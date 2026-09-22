package main

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type adminSearchFixture struct {
	server *Server
	store  *Store
}

func newAdminSearchFixture(t *testing.T) *adminSearchFixture {
	t.Helper()
	cfg := defaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "flowbridge.db")
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &adminSearchFixture{
		server: NewServer(cfg, store, nil),
		store:  store,
	}
}

func (fixture *adminSearchFixture) createTask(t *testing.T, workflowTaskID, backendTaskID string) *WorkflowTask {
	t.Helper()
	req := AnimeVideoRequest{TaskID: workflowTaskID}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal %q: %v", workflowTaskID, err)
	}
	task, err := fixture.store.CreateAnimeVideoTask(context.Background(), workflowTaskID, req, raw)
	if err != nil {
		t.Fatalf("CreateAnimeVideoTask(%q): %v", workflowTaskID, err)
	}
	if backendTaskID == "" {
		return task
	}
	if err := fixture.store.UpdateStepAccepted(context.Background(), task.ID, 1, backendTaskID, json.RawMessage(`{"status":0}`)); err != nil {
		t.Fatalf("UpdateStepAccepted(%q): %v", backendTaskID, err)
	}
	return task
}

func (fixture *adminSearchFixture) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	response := httptest.NewRecorder()
	fixture.server.ServeHTTP(response, request)
	return response
}

func TestAdminSearchRedirectsExactWorkflowTaskID(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	fixture.createTask(t, "bridge_exact_workflow", "backend-exact-workflow")

	response := fixture.get(t, "/admin/workflows?task_id=bridge_exact_workflow")
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusFound, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/admin/workflows/bridge_exact_workflow" {
		t.Fatalf("Location = %q, want workflow detail URL", location)
	}
}

func TestAdminSearchRedirectsExactBackendTaskID(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	fixture.createTask(t, "bridge_backend_owner", "backend-9d3fc0de-exact")
	fixture.createTask(t, "bridge_unrelated", "backend-unrelated")

	response := fixture.get(t, "/admin/workflows?task_id=backend-9d3fc0de-exact")
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusFound, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/admin/workflows/bridge_backend_owner" {
		t.Fatalf("Location = %q, want owning workflow detail URL", location)
	}
}

func TestAdminSearchRedirectsExactBackendUUID(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	task := fixture.createTask(t, "bridge_backend_uuid_owner", "backend-internal-id")
	if err := fixture.store.UpdateStepAccepted(context.Background(), task.ID, 1, "backend-internal-id",
		json.RawMessage(`{"uuid":"9d3fc0de-8ffa-49e9-b479-756c1c154c00"}`)); err != nil {
		t.Fatalf("UpdateStepAccepted backend UUID: %v", err)
	}

	response := fixture.get(t, "/admin/workflows?task_id=9d3fc0de-8ffa-49e9-b479-756c1c154c00")
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusFound, response.Body.String())
	}
	if location := response.Header().Get("Location"); location != "/admin/workflows/bridge_backend_uuid_owner" {
		t.Fatalf("Location = %q, want owning workflow detail URL", location)
	}
}

func TestAdminSearchFiltersByPartialWorkflowTaskID(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	fixture.createTask(t, "bridge_order_needle_a", "backend-a")
	fixture.createTask(t, "bridge_order_needle_b", "backend-b")
	fixture.createTask(t, "bridge_unrelated", "backend-c")

	response := fixture.get(t, "/admin/workflows?task_id=order_needle")
	assertAdminSearchResults(t, response, 2,
		[]string{"bridge_order_needle_a", "bridge_order_needle_b"},
		[]string{"bridge_unrelated"},
	)
}

func TestAdminSearchFiltersByPartialBackendTaskID(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	fixture.createTask(t, "bridge_backend_match_a", "render-9d3fc0de-image")
	fixture.createTask(t, "bridge_backend_match_b", "render-9d3fc0de-video")
	fixture.createTask(t, "bridge_backend_unrelated", "render-other-video")

	response := fixture.get(t, "/admin/workflows?task_id=9d3fc0de")
	assertAdminSearchResults(t, response, 2,
		[]string{"bridge_backend_match_a", "bridge_backend_match_b"},
		[]string{"bridge_backend_unrelated"},
	)
}

func TestAdminSearchFiltersByPartialBackendUUID(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	matchingTask := fixture.createTask(t, "bridge_uuid_match", "backend-uuid-match")
	if err := fixture.store.UpdateStepAccepted(context.Background(), matchingTask.ID, 1, "backend-uuid-match",
		json.RawMessage(`{"uuid":"9d3fc0de-8ffa-49e9-b479-756c1c154c00"}`)); err != nil {
		t.Fatalf("UpdateStepAccepted matching backend UUID: %v", err)
	}
	unrelatedTask := fixture.createTask(t, "bridge_uuid_unrelated", "backend-uuid-unrelated")
	if err := fixture.store.UpdateStepAccepted(context.Background(), unrelatedTask.ID, 1, "backend-uuid-unrelated",
		json.RawMessage(`{"uuid":"different-backend-uuid"}`)); err != nil {
		t.Fatalf("UpdateStepAccepted unrelated backend UUID: %v", err)
	}

	response := fixture.get(t, "/admin/workflows?task_id=9d3fc0de")
	assertAdminSearchResults(t, response, 1,
		[]string{"bridge_uuid_match"},
		[]string{"bridge_uuid_unrelated"},
	)
}

func TestAdminSearchNoMatchReturnsEmptyList(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	fixture.createTask(t, "bridge_existing_a", "backend-existing-a")
	fixture.createTask(t, "bridge_existing_b", "backend-existing-b")

	response := fixture.get(t, "/admin/workflows?task_id=definitely-not-present")
	assertAdminSearchResults(t, response, 0, nil,
		[]string{"bridge_existing_a", "bridge_existing_b"},
	)
}

func TestAdminSearchPaginationAndPageSizeKeepTaskID(t *testing.T) {
	fixture := newAdminSearchFixture(t)
	for index := 0; index < 51; index++ {
		fixture.createTask(t,
			"bridge_page_needle_"+strconv.Itoa(index),
			"backend-page-"+strconv.Itoa(index),
		)
	}
	fixture.createTask(t, "bridge_page_unrelated", "backend-page-unrelated")

	response := fixture.get(t, "/admin/workflows?task_id=page_needle&page=2&page_size=25")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "Total 51 · Page 2 / 3") {
		t.Fatalf("filtered pagination summary missing from body")
	}
	if strings.Contains(body, "bridge_page_unrelated") {
		t.Fatalf("unrelated workflow leaked into filtered page")
	}

	assertAdminPaginationLink(t, body, "1", "25", "page_needle")
	assertAdminPaginationLink(t, body, "3", "25", "page_needle")

	pageSizeForm := adminFormContaining(t, body, `id="page-size"`)
	if !strings.Contains(pageSizeForm, `name="task_id"`) || !strings.Contains(pageSizeForm, `value="page_needle"`) {
		t.Fatalf("page-size form does not preserve task_id: %s", pageSizeForm)
	}
}

func assertAdminSearchResults(t *testing.T, response *httptest.ResponseRecorder, total int, included, excluded []string) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusOK, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "Total "+strconv.Itoa(total)+" · Page 1 / 1") {
		t.Fatalf("filtered total %d missing from body", total)
	}
	for _, taskID := range included {
		if !strings.Contains(body, taskID) {
			t.Errorf("expected task %q in body", taskID)
		}
	}
	for _, taskID := range excluded {
		if strings.Contains(body, taskID) {
			t.Errorf("unexpected task %q in body", taskID)
		}
	}
}

func assertAdminPaginationLink(t *testing.T, body, page, pageSize, taskID string) {
	t.Helper()
	linkPattern := regexp.MustCompile(`href="([^"]+)"`)
	for _, match := range linkPattern.FindAllStringSubmatch(body, -1) {
		href := html.UnescapeString(match[1])
		parsed, err := url.Parse(href)
		if err != nil || parsed.Path != "/admin/workflows" {
			continue
		}
		query := parsed.Query()
		if query.Get("page") == page && query.Get("page_size") == pageSize {
			if query.Get("task_id") != taskID {
				t.Fatalf("pagination link %q lost task_id %q", href, taskID)
			}
			return
		}
	}
	t.Fatalf("pagination link for page=%s page_size=%s not found", page, pageSize)
}

func adminFormContaining(t *testing.T, body, marker string) string {
	t.Helper()
	markerIndex := strings.Index(body, marker)
	if markerIndex < 0 {
		t.Fatalf("form marker %q not found", marker)
	}
	formStart := strings.LastIndex(body[:markerIndex], "<form")
	formEndRelative := strings.Index(body[markerIndex:], "</form>")
	if formStart < 0 || formEndRelative < 0 {
		t.Fatalf("form containing %q is incomplete", marker)
	}
	return body[formStart : markerIndex+formEndRelative+len("</form>")]
}
