package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/host"
)

// ExecuteFixture executes exclusively mocked host calls in an isolated worker.
// Its filesystem is a fresh temporary directory, never the caller's workspace.
func (s *Supervisor) ExecuteFixture(ctx context.Context, m contract.Manifest, source string, tc contract.TestCase) contract.WorkerResponse {
	fail := func(err error) contract.WorkerResponse { return contract.WorkerResponse{Error: asToolError(err)} }
	if err := contract.ValidateInput(m, tc.Input); err != nil {
		return fail(err)
	}
	dir, err := os.MkdirTemp("", "knurl-fixture-")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = root.Close() }()
	mocks := tc.Mocks
	if mocks == nil {
		mocks = &contract.TestMocks{}
	}
	if mocks.FS != nil {
		for path, data := range mocks.FS.Files {
			clean, e := host.ValidateRelativePath(path)
			if e != nil {
				return fail(e)
			}
			if e = root.MkdirAll(filepath.Dir(clean), 0o700); e != nil {
				return fail(e)
			}
			if e = root.WriteFile(clean, []byte(data), 0o600); e != nil {
				return fail(e)
			}
		}
	}
	secrets := map[string]string{}
	for _, name := range mocks.Secrets {
		secrets[name] = fmt.Sprintf("knurl-fixture-secret-%s", name)
	}
	resp := s.RunWorkerRequest(ctx, contract.WorkerRequest{Manifest: m, Source: source, WorkDir: dir, Input: tc.Input, TimeoutMs: m.EffectiveTimeoutMs(), Mocks: mocks, Secrets: secrets})
	if resp.OK {
		if e := contract.ValidateOutput(m, resp.Output); e != nil {
			return fail(e)
		}
	}
	return resp
}
