package host

import (
	"context"
	"errors"
	"github.com/ViniTamanhao/knurl/internal/contract"
	"strings"
	"testing"
)

func TestMockBoundaryRedactionAndMissingLog(t *testing.T) {
	secret := "fixture-secret-value"
	m := contract.Manifest{Capabilities: contract.Capabilities{Git: &contract.GitCapability{Read: true}, HTTP: &contract.HTTPCapability{Allow: []contract.HTTPRule{{Domain: "api.example.com", Methods: []string{"GET"}}}}}}
	mocks := &contract.TestMocks{Git: &contract.GitMock{Diff: &contract.GitDiff{Patch: secret}}, HTTP: []contract.HTTPMock{{Method: "GET", URL: "https://api.example.com/", Result: &contract.HTTPMockResult{Status: 200, Headers: map[string]string{"token": secret}, Body: secret}}}}
	h, err := NewWithOptions(m, t.TempDir(), map[string]string{"TOKEN": secret}, nil, WithMocks(mocks))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	response, err := h.HTTPGet(context.Background(), "https://api.example.com/", HTTPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Headers["token"], secret) || strings.Contains(response.Body, secret) {
		t.Fatal("HTTP exposed mock secret to JS")
	}
	if mocks.HTTP[0].Result.Headers["token"] != secret {
		t.Fatal("mutated fixture")
	}
	diff, err := h.GitDiff(context.Background(), GitDiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff.Patch, secret) {
		t.Fatal("git patch exposed mock secret to JS")
	}
	if mocks.Git.Diff.Patch != secret {
		t.Fatal("mutated git mock")
	}
	_, err = h.GitLog(context.Background(), GitLogOptions{})
	var te *contract.ToolError
	if !errors.As(err, &te) || te.Code != contract.ErrCapabilityDenied {
		t.Fatalf("missing log: %v", err)
	}
	mocks.Git.Log = []contract.GitCommit{}
	log, err := h.GitLog(context.Background(), GitLogOptions{})
	if err != nil || log == nil {
		t.Fatalf("explicit empty log: %v %v", log, err)
	}
}
