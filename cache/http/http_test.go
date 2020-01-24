package http

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	testutils "github.com/buchgr/bazel-remote/utils"
)

type testServer struct {
	srv *httptest.Server

	mu  sync.Mutex
	ac  map[string][]byte
	cas map[string][]byte
}

func (s *testServer) handler(w http.ResponseWriter, r *http.Request) {

	fields := strings.Split(r.URL.Path, "/")

	kindMap := s.ac
	if fields[1] == "ac" {
		kindMap = s.ac
	} else if fields[1] == "cas" {
		kindMap = s.cas
	}
	hash := fields[2]

	s.mu.Lock()
	defer s.mu.Unlock()

	switch method := r.Method; method {
	case http.MethodGet:
		data, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		w.Write(data)

	case http.MethodPut:
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
		}
		kindMap[hash] = data

	case http.MethodHead:
		_, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
	}
}

func newTestServer() *testServer {
	ts := testServer{
		ac:  make(map[string][]byte),
		cas: make(map[string][]byte),
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(ts.handler))

	return &ts
}

func TestEverything(t *testing.T) {
	s := newTestServer()
	defer s.srv.Close()

	casData, hash := testutils.RandomDataAndHash(1024)
	acData := []byte{1, 2, 3, 4}

	acUrl := s.srv.URL + "/ac/" + hash
	casUrl := s.srv.URL + "/cas/" + hash

	// PUT two different values with the same key in ac and cas.

	req, err := http.NewRequest("PUT", acUrl, bytes.NewReader(acData))
	if err != nil {
		t.Error(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, resp.StatusCode)
	}

	req, err = http.NewRequest("PUT", casUrl, bytes.NewReader(casData))
	if err != nil {
		t.Error(err)
	}

	resp, err = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, resp.StatusCode)
	}

	// Confirm that we can HEAD both values succesfully.

	req, err = http.NewRequest("HEAD", acUrl, nil)
	if err != nil {
		t.Error(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Error("expected URL to exist")
	}

	req, err = http.NewRequest("HEAD", casUrl, nil)
	if err != nil {
		t.Error(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Error("expected URL to exist")
	}

	// Confirm that we can GET both values succesfully.

	req, err = http.NewRequest("GET", acUrl, nil)
	if err != nil {
		t.Error(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
	}

	returnedData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if bytes.Compare(returnedData, acData) != 0 {
		t.Error("Different data returned")
	}

	req, err = http.NewRequest("GET", casUrl, nil)
	if err != nil {
		t.Error(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
	}

	returnedData, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if bytes.Compare(returnedData, casData) != 0 {
		t.Error("Different data returned")
	}
}
