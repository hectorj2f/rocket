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

package cas

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/coreos/rocket/pkg/keystore"

	"github.com/appc/spec/aci"
	"github.com/appc/spec/schema/types"
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

func (r Remote) Marshal() []byte {
	m, _ := json.Marshal(r)
	return m
}
func (r *Remote) Unmarshal(data []byte) {
	err := json.Unmarshal(data, r)
	if err != nil {
		panic(err)
	}
}
func (r Remote) Hash() string {
	return types.NewHashSHA512([]byte(r.ACIURL)).String()
}
func (r Remote) Type() int64 {
	return remoteType
}

// Download downloads and verifies the remote ACI.
// If Keystore is nil signature verification will be skipped.
// bool will be True if the cached aci can be still used.
// bool will be False if there is not any cached aci or there is a newest version of it.
// Download returns the signer, an *os.File representing the ACI, and an error if any.
// err will be nil if the ACI downloads successfully and the ACI is verified.
func (r Remote) Download(ds Store, ks *keystore.Keystore) (*openpgp.Entity, *os.File, bool, error) {
	var entity *openpgp.Entity
	var err error
	acif, useCachedACI, err := downloadACI(ds, r)

	if err != nil {
		return nil, acif, useCachedACI, fmt.Errorf("error downloading the aci image: %v", err)
	}

	if useCachedACI {
		return nil, nil, useCachedACI, nil
	}

	if ks != nil {
		sigTempFile, err := downloadSignatureFile(r.SigURL)
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
	// Recover the remote stored information in downloadACI
	ds.ReadIndex(&r)

	r.BlobKey = key
	ds.WriteIndex(&r)
	return &r, nil
}

func downloadACI(ds Store, r Remote) (*os.File, bool, error) {
	var useCachedACI = false
	client := &http.Client{}
	req, err := http.NewRequest("GET", r.ACIURL, nil)
	if r.ETag != "" {
		req.Header.Add("If-None-Match", r.ETag)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, useCachedACI, err
	}
	defer res.Body.Close()

	prefix := "Downloading aci"
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

	// TODO(jonboulle): handle http more robustly (redirects?)
	if res.StatusCode == http.StatusNotModified || res.StatusCode == http.StatusOK {
		r.ETag = res.Header.Get("ETag")
		r.CacheControl = NewCache(res.Header.Get("Cache-Control"))
		ds.WriteIndex(&r)

		useCachedACI = (res.StatusCode == http.StatusNotModified)
		if useCachedACI {
			return nil, useCachedACI, nil
		}

		reader := &ioprogress.Reader{
			Reader:       res.Body,
			Size:         res.ContentLength,
			DrawFunc:     ioprogress.DrawTerminalf(os.Stdout, fmtfunc),
			DrawInterval: time.Second,
		}

		aciTempFile, err := ds.tmpFile()
		if err != nil {
			return nil, useCachedACI, fmt.Errorf("error downloading aci: %v", err)
		}

		if _, err := io.Copy(aciTempFile, reader); err != nil {
			aciTempFile.Close()
			os.Remove(aciTempFile.Name())
			return nil, useCachedACI, fmt.Errorf("error copying temp aci: %v", err)
		}
		if err := aciTempFile.Sync(); err != nil {
			aciTempFile.Close()
			os.Remove(aciTempFile.Name())
			return nil, useCachedACI, fmt.Errorf("error writing temp aci: %v", err)
		}

		return aciTempFile, useCachedACI, nil

	} else {
		return nil, useCachedACI, fmt.Errorf("bad HTTP status code %d with message: %s \n", res.StatusCode, http.StatusText(res.StatusCode))
	}

}

func downloadSignatureFile(sigurl string) (*os.File, error) {
	sig, err := ioutil.TempFile("", "")
	if err != nil {
		return nil, fmt.Errorf("error downloading signature: %v", err)
	}
	res, err := http.Get(sigurl)
	if err != nil {
		return nil, fmt.Errorf("error downloading signature: %v", err)
	}
	defer res.Body.Close()

	// TODO(jonboulle): handle http more robustly (redirects?)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad HTTP status code: %d", res.StatusCode)
	}

	if _, err := io.Copy(sig, res.Body); err != nil {
		sig.Close()
		os.Remove(sig.Name())
		return nil, fmt.Errorf("error copying signature: %v", err)
	}
	if err := sig.Sync(); err != nil {
		sig.Close()
		os.Remove(sig.Name())
		return nil, fmt.Errorf("error writing signature: %v", err)
	}
	return sig, nil
}
