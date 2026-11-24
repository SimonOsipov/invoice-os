package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestERPCallbackPage_HeadNeverCallsTheWorker(t *testing.T) {
	e := ecStart(t, ecOutcome("connected"), nil)
	req := httptest.NewRequest(http.MethodHead, "/erp/callback?state="+ecState()+"&code=c", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if n := e.w.count(); n != 0 {
		t.Errorf("HEAD made %d worker calls, want 0 (a link scanner must not spend the state)", n)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD answered a body of %d bytes", rec.Body.Len())
	}
}
