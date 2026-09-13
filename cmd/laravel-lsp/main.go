package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/tliron/commonlog"
	_ "github.com/tliron/commonlog/simple"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"

	"github.com/akyrey/laravel-lsp/internal/lsp"
)

const lsName = "laravel-lsp"

// version is the fallback build stamp, overridden at link time by the
// Makefile and the release workflow via -X main.version. Builds produced by
// "go install <module>@<tag>" cannot receive ldflags, so resolveVersion falls
// back to the module version the toolchain records in the build info.
var version = ""

const devVersion = "0.0.0-dev"

// resolveVersion reports the most specific version available: the link-time
// stamp when set, otherwise the module version recorded by the toolchain, and
// devVersion when neither is present (a plain "go build" from a source tree).
func resolveVersion() string {
	if version != "" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return devVersion
	}
	// The toolchain records "(devel)" for builds from a working tree rather
	// than from a resolved module version; that is no more useful than our own
	// placeholder.
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return devVersion
}

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "debug":
			runDebug(os.Args[2:])
			return
		case "version", "--version", "-version":
			fmt.Println(resolveVersion())
			return
		case "help", "--help", "-help", "-h":
			fmt.Fprintf(os.Stderr, "Usage: laravel-lsp [debug [flags] [project-root]]\n\nRun without arguments to start the LSP server over stdio.\nRun 'laravel-lsp debug --help' for the index inspection tool.\n")
			return
		}
	}

	// Default: run LSP server over stdio.
	commonlog.Configure(1, nil)
	s := lsp.NewServer(commonlog.GetLogger(lsName), resolveVersion())
	handler := buildHandler(s)
	srv := server.NewServer(handler, lsName, false)
	if err := srv.RunStdio(); err != nil {
		commonlog.GetLogger(lsName).Errorf("%s", err)
	}
}

func buildHandler(s *lsp.Server) *protocol.Handler {
	return &protocol.Handler{
		Initialize:  s.Initialize,
		Initialized: s.Initialized,
		Shutdown:    s.Shutdown,
		SetTrace:    s.SetTrace,

		TextDocumentDidOpen:   s.DidOpen,
		TextDocumentDidChange: s.DidChange,
		TextDocumentDidClose:  s.DidClose,

		TextDocumentDefinition:     s.Definition,
		TextDocumentReferences:     s.References,
		TextDocumentHover:          s.Hover,
		TextDocumentCompletion:     s.Completion,
		TextDocumentRename:         s.Rename,
		TextDocumentPrepareRename:  s.PrepareRename,
		TextDocumentCodeAction:     s.CodeAction,
		TextDocumentDocumentSymbol: s.DocumentSymbol,
		WorkspaceSymbol:            s.WorkspaceSymbol,
	}
}
