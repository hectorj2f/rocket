package cas

import (
  "io/ioutil"
  "os"
  "testing"
  "time"

  "github.com/coreos/rocket/cas"
  )

const tstprefix = "cache-test"
const tstremote = "https://github.com/coreos/etcd/releases/download/v0.5.0-alpha.4/etcd-v0.5.0-alpha.4-linux-amd64.aci"

func TestCacheControl(t *testing.T) {
  cc1 := NewCache("max-age=10 no-cache no-store")
  if !cc1.NoStore {
    t.Errorf("expected a no-store header argument for cache-control")
  }
  if !cc1.NoCache {
    t.Errorf("expected a no-cache header argument for cache-control")
  }
  if cc1.MaxAge != 10 {
    t.Errorf("expected max-age header argument for cache-control")
  }

  cc2 := NewCache("max-age=10")
  // Sleep during 11 seconds
  time.Sleep(11000 * time.Millisecond)
  if cc2.UseCachedImage() {
    t.Errorf("expected max-age header argument to be expired after sleep time")
  }
  if cc2.NoStore {
    t.Errorf("unexpected a no-store header argument to be false")
  }
  if cc2.NoCache {
    t.Errorf("unexpected a no-cache header argument to be false")
  }
}

func TestImageCacheControl(t *testing.T) {
  dir, err := ioutil.TempDir("", tstprefix)
  if err != nil {
    t.Fatalf("error creating tempdir: %v", err)
  }
  defer os.RemoveAll(dir)
  ds := NewStore(dir)

  rem := NewRemote("wrongEndpoint", []string{})
  rem, err = rem.Download(*ds)
  if rem != nil && err == nil {
    t.Fatalf("expected error when downloading an image")
  }

  rem = NewRemote(tstremote, []string{})
  rem, err = rem.Download(*ds)
  if err != nil {
    t.Fatalf("Error downloading: %v\n", err)
  }

  if rem.ETag == "" {
    t.Errorf("expected remote to have a ETag header argument")
  }
  if rem.Blob == "" {
    t.Errorf("expected remote to download an image")
  }
  if rem.CacheControl.NoStore {
    t.Errorf("expected a no-store header argument to be false")
  }
  if rem.CacheControl.NoCache {
    t.Errorf("expected a no-cache header argument to be false")
  }
  if rem.CacheControl.MaxAge <= 0 {
    t.Errorf("expected max-age header argument to be greater than 0")
  }
}