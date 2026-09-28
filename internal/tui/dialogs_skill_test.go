package tui

import (
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// The list draws a category header wherever the category changes, so local
// and library skills must come out grouped, not interleaved by name.
func TestSkillItemsGroupLocalBeforeLibrary(t *testing.T) {
	items := (&App{}).skillItems([]client.Skill{
		{Name: "zeta"},
		{Name: "go-dev", Source: "library-plugin"},
		{Name: "alpha"},
		{Name: "api-design", Source: "library-plugin"},
		{Name: "mid"},
	})
	var got []string
	for _, item := range items {
		got = append(got, item.Category+":"+item.Label)
	}
	want := "Skills:alpha,Skills:mid,Skills:zeta,Library:api-design,Library:go-dev"
	if strings.Join(got, ",") != want {
		t.Fatalf("items = %s\nwant    %s", strings.Join(got, ","), want)
	}
}
