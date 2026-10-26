package explorer

import (
	"io/fs"
	"strings"
	"testing"
)

func TestFilesContainsIndexAtTheRoot(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		_, err := fs.Stat(Files(), name)
		if err != nil {
			t.Errorf("Files() has no %s at the root: %v", name, err)
		}
	}
}

func TestAssetsHaveNoDynamicHTMLNorExternalURL(t *testing.T) {
	banned := []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "://"}

	err := fs.WalkDir(Files(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := fs.ReadFile(Files(), path)
		if err != nil {
			return err
		}

		for _, b := range banned {
			if strings.Contains(string(data), b) {
				t.Errorf("%s contains %q", path, b)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIndexReferencesAssetsByRelativePath(t *testing.T) {
	data, err := fs.ReadFile(Files(), "index.html")
	if err != nil {
		t.Fatal(err)
	}

	html := string(data)
	for _, ref := range []string{`src="app.js"`, `href="style.css"`} {
		if !strings.Contains(html, ref) {
			t.Errorf("index.html does not contain %s", ref)
		}
	}

	if strings.Contains(html, "<script>") || strings.Contains(html, "<style") || strings.Contains(html, " style=") {
		t.Error("index.html has inline script or style, which the CSP forbids")
	}
}
