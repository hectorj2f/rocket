// Copyright 2014 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package cas implements a content-addressable-store on disk.
// It leverages the `diskv` package to store items in a simple
// key-value blob store: https://github.com/peterbourgon/diskv
package cas

import (
	"database/sql"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/rocket/pkg/keystore"

	"github.com/coreos/rocket/Godeps/_workspace/src/github.com/appc/docker2aci/lib"
	"github.com/coreos/rocket/Godeps/_workspace/src/github.com/appc/spec/aci"

	"github.com/coreos/rocket/Godeps/_workspace/src/github.com/mitchellh/ioprogress"
	"github.com/coreos/rocket/Godeps/_workspace/src/golang.org/x/crypto/openpgp"
)

func NewRemote(aciurl, sigurl string) *Remote {
	r := &Remote{
		ACIURL: aciurl,
		SigURL: sigurl,
	}
	return r
}

type Remote struct {
	ACIURL string
	SigURL string
	ETag   string
	// The key in the blob store under which the ACI has been saved.
	BlobKey      string
	CacheControl *CacheControl
}

func (r Remote) Download(ds Store, ks *keystore.Keystore) (*openpgp.Entity, *os.File, bool, error) {
	var entity *openpgp.Entity
	u, err := url.Parse(r.ACIURL)
	if err != nil {
		return nil, nil, false, fmt.Errorf("error parsing ACI url: %v", err)
	}
	if u.Scheme == "docker" {
		registryURL := strings.TrimPrefix(r.ACIURL, "docker://")

		tmpDir, err := ds.tmpDir()
		if err != nil {
			return nil, nil, false, fmt.Errorf("error creating temporary dir for docker to ACI conversion: %v", err)
		}

		acis, err := docker2aci.Convert(registryURL, true, tmpDir)
		if err != nil {
			return nil, nil, false, fmt.Errorf("error converting docker image to ACI: %v", err)
		}

		aciFile, err := os.Open(acis[0])
		if err != nil {
			return nil, nil, false, fmt.Errorf("error opening squashed ACI file: %v", err)
		}

		return nil, aciFile, false, nil
	}

	acif, useCachedACI, err := downloadACI(ds, r)
	if err != nil {
		return nil, acif, useCachedACI, fmt.Errorf("error downloading the aci image: %v", err)
	}
	if useCachedACI {
		return nil, nil, useCachedACI, nil
	}

	if ks != nil {
		fmt.Printf("Downloading signature from %v\n", r.SigURL)
		sigTempFile, err := downloadSignatureFile(ds, r.SigURL, r)
		if err != nil {
			return nil, acif, useCachedACI, fmt.Errorf("error downloading the signature file: %v", err)
		}
		defer sigTempFile.Close()
		defer os.Remove(sigTempFile.Name())

		manifest, err := aci.ManifestFromImage(acif)
		if err != nil {
			return nil, acif, useCachedACI, err
		}

		if _, err := acif.Seek(0, 0); err != nil {
			return nil, acif, useCachedACI, err
		}
		if _, err := sigTempFile.Seek(0, 0); err != nil {
			return nil, acif, useCachedACI, err
		}
		if entity, err = ks.CheckSignature(manifest.Name.String(), acif, sigTempFile); err != nil {
			return nil, acif, useCachedACI, err
		}
	}

	if _, err := acif.Seek(0, 0); err != nil {
		return nil, acif, useCachedACI, err
	}
	return entity, acif, useCachedACI, nil
}

// TODO: add locking
// Store stores the ACI represented by r in the target data store.
func (r Remote) Store(ds Store, aci io.Reader) (*Remote, error) {
	key, err := ds.WriteACI(aci)
	if err != nil {
		return nil, err
	}
	r, _, err = ds.GetRemote(r.ACIURL)
	if err != nil {
		return nil, err
	}

	r.BlobKey = key
	err = ds.WriteRemote(&r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// downloadACI gets the aci specified at aciurl
func downloadACI(ds Store, r Remote) (*os.File, bool, error) {
	var (
		useCachedACI = false
		aciurl       = r.ACIURL
	)
	tmp, err := ds.tmpFile()
	if err != nil {
		return nil, useCachedACI, fmt.Errorf("error downloading ACI: %v", err)
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
			tmp.Close()
		}
	}()

	client := &http.Client{}
	req, err := http.NewRequest("GET", aciurl, nil)
	if r.ETag != "" {
		req.Header.Add("If-None-Match", r.ETag)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, useCachedACI, err
	}
	defer res.Body.Close()

	prefix := "Downloading ACI"
	fmtBytesSize := 18
	barSize := int64(80 - len(prefix) - fmtBytesSize)
	bar := ioprogress.DrawTextFormatBar(barSize)
	fmtfunc := func(progress, total int64) string {
		return fmt.Sprintf(
			"%s: %s %s",
			prefix,
			bar(progress, total),
			ioprogress.DrawTextFormatBytes(progress, total),
		)
	}

	reader := &ioprogress.Reader{
		Reader:       res.Body,
		Size:         res.ContentLength,
		DrawFunc:     ioprogress.DrawTerminalf(os.Stdout, fmtfunc),
		DrawInterval: time.Second,
	}

	// TODO(jonboulle): handle http more robustly (redirects?)
	if res.StatusCode == http.StatusNotModified || res.StatusCode == http.StatusOK {
		r.ETag = res.Header.Get("ETag")
		r.CacheControl = NewCache(res.Header.Get("Cache-Control"))

		useCachedACI = (res.StatusCode == http.StatusNotModified)
		if useCachedACI {
			return nil, useCachedACI, nil
		}

		if _, err := io.Copy(tmp, reader); err != nil {
			return nil, useCachedACI, fmt.Errorf("error copying ACI: %v", err)
		}

		if err := tmp.Sync(); err != nil {
			return nil, useCachedACI, fmt.Errorf("error writing ACI: %v", err)
		}
	} else {
		return nil, useCachedACI, fmt.Errorf("bad HTTP status code: %d", res.StatusCode)
	}

	if err = ds.WriteRemote(&r); err != nil {
		return nil, useCachedACI, err
	}

	return tmp, useCachedACI, nil
}

// downloadSignatureFile gets the signature specified at sigurl
func downloadSignatureFile(ds Store, sigurl string, r Remote) (*os.File, error) {
	tmp, err := ioutil.TempFile("", "")
	if err != nil {
		return nil, fmt.Errorf("error downloading signature: %v", err)
	}
	defer func() {
		if err != nil {
			os.Remove(tmp.Name())
			tmp.Close()
		}
	}()

	res, err := http.Get(sigurl)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	prefix := "Downloading signature"
	fmtBytesSize := 18
	barSize := int64(80 - len(prefix) - fmtBytesSize)
	bar := ioprogress.DrawTextFormatBar(barSize)
	fmtfunc := func(progress, total int64) string {
		return fmt.Sprintf(
			"%s: %s %s",
			prefix,
			bar(progress, total),
			ioprogress.DrawTextFormatBytes(progress, total),
		)
	}

	reader := &ioprogress.Reader{
		Reader:       res.Body,
		Size:         res.ContentLength,
		DrawFunc:     ioprogress.DrawTerminalf(os.Stdout, fmtfunc),
		DrawInterval: time.Second,
	}

	// TODO(jonboulle): handle http more robustly (redirects?)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad HTTP status code: %d", res.StatusCode)
	}

	if _, err := io.Copy(tmp, reader); err != nil {
		return nil, fmt.Errorf("error copying signature: %v", err)
	}

	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("error writing signature: %v", err)
	}

	return tmp, nil
}

// GetRemote tries to retrieve a remote with the given aciURL. found will be
// false if remote doesn't exist.
func GetRemote(tx *sql.Tx, aciURL string) (remote *Remote, found bool, err error) {
	remote = &Remote{}
	rows, err := tx.Query("SELECT sigurl, etag, blobkey, maxage, downloaded FROM remote WHERE aciurl == $1", aciURL)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		found = true
		cc := &CacheControl{}
		if err := rows.Scan(&remote.SigURL, &remote.ETag, &remote.BlobKey, &cc.MaxAge, &cc.Downloaded); err != nil {
			return nil, false, err
		}
		remote.CacheControl = cc
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	return remote, found, err
}

// WriteRemote adds or updates the provided Remote.
func WriteRemote(tx *sql.Tx, remote *Remote) error {
	// ql doesn't have an INSERT OR UPDATE function so
	// it's faster to remove and reinsert the row
	_, err := tx.Exec("DELETE FROM remote WHERE aciurl == $1", remote.ACIURL)
	if err != nil {
		return err
	}

	if remote.CacheControl != nil {
		_, err = tx.Exec("INSERT INTO remote VALUES ($1, $2, $3, $4, $5, $6)",
			remote.ACIURL,
			remote.SigURL,
			remote.ETag,
			remote.BlobKey,
			remote.CacheControl.MaxAge,
			remote.CacheControl.Downloaded)
	}
	if err != nil {
		return err
	}
	return nil
}
