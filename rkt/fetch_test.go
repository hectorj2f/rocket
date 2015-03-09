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

package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/coreos/rocket/cas"
	"github.com/coreos/rocket/pkg/aci"
	"github.com/coreos/rocket/pkg/keystore"
	"github.com/coreos/rocket/pkg/keystore/keystoretest"

	"github.com/coreos/rocket/Godeps/_workspace/src/github.com/appc/spec/discovery"
)

const (
	StatusNotModified = 304
)

func TestNewDiscoveryApp(t *testing.T) {
	tests := []struct {
		in string

		w *discovery.App
	}{
		// not a valid AC name
		{
			"bad AC name",
			nil,
		},
		// simple case - default arch, os should be substituted
		{
			"foo.com/bar",
			&discovery.App{
				Name: "foo.com/bar",
				Labels: map[string]string{
					"arch": defaultArch,
					"os":   defaultOS,
				},
			},
		},
		// overriding arch, os should work
		{
			"www.abc.xyz/my/app,os=freebsd,arch=i386",
			&discovery.App{
				Name: "www.abc.xyz/my/app",
				Labels: map[string]string{
					"arch": "i386",
					"os":   "freebsd",
				},
			},
		},
		// setting version should work
		{
			"yes.com/no:v1.2.3",
			&discovery.App{
				Name: "yes.com/no",
				Labels: map[string]string{
					"version": "v1.2.3",
					"arch":    defaultArch,
					"os":      defaultOS,
				},
			},
		},
		// arbitrary user-supplied labels
		{
			"example.com/foo/haha,val=one",
			&discovery.App{
				Name: "example.com/foo/haha",
				Labels: map[string]string{
					"val":  "one",
					"arch": defaultArch,
					"os":   defaultOS,
				},
			},
		},
		// combinations
		{
			"one.two/appname:three,os=four,foo=five,arch=six",
			&discovery.App{
				Name: "one.two/appname",
				Labels: map[string]string{
					"version": "three",
					"os":      "four",
					"foo":     "five",
					"arch":    "six",
				},
			},
		},
	}
	for i, tt := range tests {
		g := newDiscoveryApp(tt.in)
		if !reflect.DeepEqual(g, tt.w) {
			t.Errorf("#%d: got %v, want %v", i, g, tt.w)
		}
	}
}

func TestFetchImage(t *testing.T) {
	dir, err := ioutil.TempDir("", "fetch-image")
	if err != nil {
		t.Fatalf("error creating tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	ds, err := cas.NewStore(dir)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	defer ds.Dump(false)

	ks, ksPath, err := keystore.NewTestKeystore()
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}
	defer os.RemoveAll(ksPath)

	key := keystoretest.KeyMap["example.com/app"]
	if _, err := ks.StoreTrustedKeyPrefix("example.com/app", bytes.NewBufferString(key.ArmoredPublicKey)); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	a, err := aci.NewBasicACI(dir, "example.com/app")
	defer a.Close()
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	// Rewind the ACI
	if _, err := a.Seek(0, 0); err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	sig, err := aci.NewDetachedSignature(key.ArmoredPrivateKey, a)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	// Rewind the ACI.
	if _, err := a.Seek(0, 0); err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Ext(r.URL.Path) {
		case ".aci":
			io.Copy(w, a)
			return
		case ".sig":
			io.Copy(w, sig)
			return
		}
	}))
	defer ts.Close()
	_, err = fetchImage(fmt.Sprintf("%s/app.aci", ts.URL), ds, ks, true)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestFetchImageCache(t *testing.T) {
	dir, err := ioutil.TempDir("", "fetch-image-cache")
	if err != nil {
		t.Fatalf("error creating tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	ds := cas.NewStore(dir)
	defer ds.Dump(false)

	ks, ksPath, err := keystore.NewTestKeystore()
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}
	defer os.RemoveAll(ksPath)

	key := keystoretest.KeyMap["example.com/app"]
	if _, err := ks.StoreTrustedKeyPrefix("example.com/app", bytes.NewBufferString(key.ArmoredPublicKey)); err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	aci, err := util.NewACI("example.com/app")
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	// Rewind the ACI
	if _, err := aci.Seek(0, 0); err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	sig, err := util.NewDetachedSignature(key.ArmoredPrivateKey, aci)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	// Rewind the ACI.
	if _, err := aci.Seek(0, 0); err != nil {
		t.Fatalf("unexpected error %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=10")
		w.Header().Set("ETag", "123456789")

		switch filepath.Ext(r.URL.Path) {
		case ".aci":
			if cc := r.Header.Get("If-None-Match"); cc == "123456789" {
				w.WriteHeader(StatusNotModified)
			} else {
				io.Copy(w, aci)
			}
			return
		case ".sig":
			io.Copy(w, sig)
			return
		}
	}))
	defer ts.Close()

	urlRemote, _ := url.Parse(fmt.Sprintf("%s/app.aci", ts.URL))
	rem.BlobKey, err = downloadImage(urlRemote.String(), sigURLFromImgURL(urlRemote.String()), rem, ds, ks)
	if err != nil {
		t.Fatalf("Error downloading image from: %v\n", err)
	}
	if rem.BlobKey == "" {
		t.Errorf("expected remote to download an image")
	}
	// Recover Remote information for validation
	rem, ok, err := ds.GetRemote(urlRemote.String())
	if err != nil {
		t.Fatalf("Error getting remote info: %v\n", err)
	}
	if rem.ETag != "123456789" {
		t.Errorf("expected remote to have a ETag header argument")
	}
	if rem.CacheControl.MaxAge != 10 {
		t.Errorf("expected max-age header argument to be '10'")
	}

	// Test download of a cached image when using If-None-Match header
	cachedBlobKey := rem.BlobKey
	rem.BlobKey, err = downloadImage(urlRemote.String(), sigURLFromImgURL(urlRemote.String()), rem, ds, ks)
	if err != nil {
		t.Fatalf("Error downloading image from %s: %v\n", ts.URL, err)
	}
	if rem.BlobKey != cachedBlobKey {
		t.Errorf("expected remote to download an image")
	}
	// Recover Remote information for validation
	rem, ok, err := ds.GetRemote(urlRemote.String())
	if err != nil {
		t.Fatalf("Error getting remote info: %v\n", err)
	}
	if rem.ETag != "123456789" {
		t.Errorf("expected remote to have a ETag header argument")
	}
	if rem.CacheControl.MaxAge != 10 {
		t.Errorf("expected max-age header argument to be '10'")
	}
}

func TestSigURLFromImgURL(t *testing.T) {
	tests := []struct {
		in, out string
	}{
		{
			"http://localhost/aci-latest-linux-amd64.aci",
			"http://localhost/aci-latest-linux-amd64.sig",
		},
	}
	for i, tt := range tests {
		out := sigURLFromImgURL(tt.in)
		if out != tt.out {
			t.Errorf("#%d: got %v, want %v", i, out, tt.out)
		}
	}
}
