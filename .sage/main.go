package main

import (
	"bytes"
	"context"
	"strings"

	"go.einride.tech/sage/sg"
	"go.einride.tech/sage/tools/sgbackstage"
	"go.einride.tech/sage/tools/sgconvco"
	"go.einride.tech/sage/tools/sggit"
	"go.einride.tech/sage/tools/sggo"
	"go.einride.tech/sage/tools/sggolangcilintv2"
	"go.einride.tech/sage/tools/sgmdformat"
	"go.einride.tech/sage/tools/sgyamlfmt"
)

func main() {
	sg.GenerateMakefiles(
		sg.Makefile{
			Path:          sg.FromGitRoot("Makefile"),
			DefaultTarget: Default,
		},
	)
}

func Default(ctx context.Context) error {
	sg.Deps(ctx, ConvcoCheck)
	sg.Deps(ctx, FormatMarkdown, FormatYaml)
	sg.Deps(ctx, GoLint)
	sg.Deps(ctx, GoTest)
	sg.Deps(ctx, GoModTidy)
	sg.Deps(ctx, GitVerifyNoDiff)
	return nil
}

func GoModTidy(ctx context.Context) error {
	sg.Logger(ctx).Println("tidying Go module files...")
	return sg.Command(ctx, "go", "mod", "tidy", "-v").Run()
}

func GoBuild(ctx context.Context) error {
	sg.Logger(ctx).Println("building the gh extension binary...")
	return sg.Command(ctx, "go", "build", "-o", "gh-dependabot-triage", ".").Run()
}

// Install builds the extension and installs it with gh, or rebuilds it if it is installed from this checkout already.
// A local extension runs the binary in the checkout, so rebuilding is all an upgrade takes.
func Install(ctx context.Context) error {
	sg.Deps(ctx, GoBuild)
	var installed bytes.Buffer
	list := sg.Command(ctx, "gh", "extension", "list")
	list.Stdout = &installed
	if err := list.Run(); err != nil {
		return err
	}
	for line := range strings.Lines(installed.String()) {
		if strings.HasPrefix(line, "gh dependabot-triage\t") {
			sg.Logger(ctx).Println("extension is installed already, rebuilt it in place")
			return nil
		}
	}
	sg.Logger(ctx).Println("installing the gh extension...")
	return sg.Command(ctx, "gh", "extension", "install", ".").Run()
}

func GoTest(ctx context.Context) error {
	sg.Logger(ctx).Println("running Go tests...")
	return sggo.TestCommand(ctx).Run()
}

func GoLint(ctx context.Context) error {
	sg.Logger(ctx).Println("linting Go files...")
	return sggolangcilintv2.Run(ctx, sggolangcilintv2.Config{})
}

func GoLintFix(ctx context.Context) error {
	sg.Logger(ctx).Println("fixing Go files...")
	return sggolangcilintv2.Fix(ctx, sggolangcilintv2.Config{})
}

func GoFormat(ctx context.Context) error {
	sg.Logger(ctx).Println("formatting Go files...")
	return sggolangcilintv2.Fmt(ctx, sggolangcilintv2.Config{})
}

func FormatMarkdown(ctx context.Context) error {
	sg.Logger(ctx).Println("formatting Markdown files...")
	return sgmdformat.Command(ctx).Run()
}

func FormatYaml(ctx context.Context) error {
	sg.Logger(ctx).Println("formatting Yaml files...")
	return sgyamlfmt.Run(ctx)
}

func BackstageValidate(ctx context.Context) error {
	sg.Logger(ctx).Println("validating Backstage files...")
	return sgbackstage.Validate(ctx)
}

func ConvcoCheck(ctx context.Context) error {
	sg.Logger(ctx).Println("checking git commits...")
	return sgconvco.Command(ctx, "check", "origin/master..HEAD").Run()
}

func GitVerifyNoDiff(ctx context.Context) error {
	sg.Logger(ctx).Println("verifying that git has no diff...")
	return sggit.VerifyNoDiff(ctx)
}
