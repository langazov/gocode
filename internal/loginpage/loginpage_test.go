package loginpage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testForm() Form {
	return Form{
		Title:  "Log in",
		Submit: "Log in",
		Fields: []Field{
			{Name: "email", Label: "Email", Type: "email", Required: true},
			{Name: "password", Label: "Password", Type: "password", Required: true},
		},
	}
}

func post(t *testing.T, target string, values url.Values, origin string) (int, string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

func TestPageRetriesThenSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	attempts := 0
	page, err := Serve(ctx, testForm(), func(_ context.Context, v map[string]string) (string, error) {
		attempts++
		if v["password"] != "right" {
			return "", errors.New("invalid email or password")
		}
		return "Signed in as " + v["email"], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	if !strings.HasPrefix(page.URL, "http://127.0.0.1:") {
		t.Fatalf("page must be loopback-only: %s", page.URL)
	}

	response, err := http.Get(page.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), `name="password"`) {
		t.Fatalf("form not rendered: %s", body)
	}

	parsed, _ := url.Parse(page.URL)
	origin := "http://" + parsed.Host
	_, body2 := post(t, page.URL, url.Values{"email": {"a@b.co"}, "password": {"wrong-secret"}}, origin)
	if !strings.Contains(body2, "invalid email or password") || !strings.Contains(body2, `value="a@b.co"`) {
		t.Fatalf("error page should show the error and keep the email: %s", body2)
	}
	if strings.Contains(body2, "wrong-secret") {
		t.Fatal("a failed submission must not echo the password")
	}

	_, body3 := post(t, page.URL, url.Values{"email": {"a@b.co"}, "password": {"right"}}, origin)
	if !strings.Contains(body3, "Signed in as a@b.co") {
		t.Fatalf("success page: %s", body3)
	}
	if err := page.Wait(ctx); err != nil {
		t.Fatalf("Wait after success: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts %d", attempts)
	}
}

func TestPageRefusesCrossOriginAndWrongPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	page, err := Serve(ctx, testForm(), func(context.Context, map[string]string) (string, error) {
		called = true
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()

	if status, _ := post(t, page.URL, url.Values{"email": {"a@b.co"}, "password": {"x"}}, "https://evil.example"); status != http.StatusForbidden {
		t.Fatalf("cross-origin post: status %d", status)
	}
	parsed, _ := url.Parse(page.URL)
	if status, _ := post(t, "http://"+parsed.Host+"/guess", url.Values{}, ""); status != http.StatusNotFound {
		t.Fatalf("wrong path: status %d", status)
	}
	if called {
		t.Fatal("a refused submission reached submit")
	}
}

func TestPageWaitEndsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	page, err := Serve(ctx, testForm(), func(context.Context, map[string]string) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	cancel()
	if err := page.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait after cancel: %v", err)
	}
}
