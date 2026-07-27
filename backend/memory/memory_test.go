package memory

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRememberMergesEquivalentProjectMemory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	project := "/tmp/demo"
	first, err := Remember(project, "  Use pnpm for workspace installs  ", "workflow", []string{"package.json"}, []string{"pnpm"})
	if err != nil {
		t.Fatalf("first remember failed: %v", err)
	}

	second, err := Remember(project, "use   pnpm for workspace installs", "workflow", []string{"pnpm-workspace.yaml"}, []string{"workspace"})
	if err != nil {
		t.Fatalf("second remember failed: %v", err)
	}

	if first.ID != second.ID {
		t.Fatalf("expected same memory id, got %s and %s", first.ID, second.ID)
	}
	if second.Strength <= first.Strength {
		t.Fatalf("expected strength to increase, got %d -> %d", first.Strength, second.Strength)
	}
	if len(second.Files) != 2 {
		t.Fatalf("expected merged files, got %#v", second.Files)
	}
	if len(second.Concepts) != 2 {
		t.Fatalf("expected merged concepts, got %#v", second.Concepts)
	}
}

func TestRenderContextBuildsCompactProjectProfile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)

	project := "/tmp/app"
	entries := []struct {
		content  string
		memType  string
		files    []string
		concepts []string
	}{
		{"Auth middleware must trim bearer tokens before verification", "bug", []string{"internal/auth/middleware.go"}, []string{"auth", "token"}},
		{"Prefer repository-service-handler layering for new modules", "architecture", []string{"internal/user/service.go"}, []string{"layering", "service"}},
		{"Use make dev to start the full workspace", "workflow", []string{"Makefile"}, []string{"dev"}},
	}
	for _, entry := range entries {
		if _, err := Remember(project, entry.content, entry.memType, entry.files, entry.concepts); err != nil {
			t.Fatalf("remember failed: %v", err)
		}
	}

	ctx := RenderContext(project, "auth token", 400)
	if ctx == "" {
		t.Fatal("expected non-empty context")
	}
	if !strings.Contains(ctx, "关键概念") {
		t.Fatalf("expected project profile concepts in context, got: %s", ctx)
	}
	if !strings.Contains(ctx, "[bug]") {
		t.Fatalf("expected detailed memory items in context, got: %s", ctx)
	}
	if !strings.Contains(ctx, "[architecture]") {
		t.Fatalf("expected architecture memory in context, got: %s", ctx)
	}
	if len([]rune(ctx)) > 400 {
		t.Fatalf("expected context to respect budget, got %d runes", len([]rune(ctx)))
	}
}

func TestEnsureProjectMemorySilentlySeedsOnlyOnce(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("HOME", tmp)
	warmedCaches = sync.Map{}

	project := filepath.Join(tmp, "demo-app")
	if err := os.MkdirAll(filepath.Join(project, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/demo\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("# Demo App\n\nA compact test project.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make(chan ProjectCacheResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := EnsureProjectMemory(project)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent ensure failed: %v", err)
	}
	initialized := 0
	for result := range results {
		if result.Initialized {
			initialized++
		}
		if result.MemoryCount != 2 {
			t.Fatalf("expected two cached memories, got %#v", result)
		}
	}
	if initialized == 0 {
		t.Fatal("expected one call to initialize the cache")
	}

	memories, err := Recall(project, "", 10)
	if err != nil {
		t.Fatalf("recall failed: %v", err)
	}
	if len(memories) != 2 {
		t.Fatalf("expected no duplicate seed memories, got %d", len(memories))
	}
}
