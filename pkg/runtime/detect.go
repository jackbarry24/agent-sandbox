package runtime

import (
	"os"
	"path/filepath"
)

type Language string

const (
	LangUnknown Language = "unknown"
	LangGo      Language = "go"
	LangNode    Language = "node"
	LangPython  Language = "python"
	LangRust    Language = "rust"
	LangJVM     Language = "jvm"
)

func DetectRepoLang(repoPath string) Language {
	files := []struct {
		name string
		lang Language
	}{
		{"go.mod", LangGo},
		{"package.json", LangNode},
		{"pyproject.toml", LangPython},
		{"requirements.txt", LangPython},
		{"Cargo.toml", LangRust},
		{"pom.xml", LangJVM},
		{"build.gradle", LangJVM},
	}
	for _, f := range files {
		if exists(filepath.Join(repoPath, f.name)) {
			return f.lang
		}
	}
	return LangUnknown
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
