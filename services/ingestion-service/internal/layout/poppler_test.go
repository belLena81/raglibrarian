package layout

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type layoutRunnerStub struct {
	output []byte
	path   string
	args   []string
}

func (runner *layoutRunnerStub) Run(_ context.Context, path string, args []string, _ int64) ([]byte, error) {
	runner.path = path
	runner.args = append([]string(nil), args...)
	return runner.output, nil
}

func TestPopplerAnalyzerParsesBoundedBBoxLayout(t *testing.T) {
	runner := &layoutRunnerStub{output: []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Untrusted metadata title</title><meta name="Producer" content="Poppler"/></head><body><doc><page width="600" height="800"><flow><block xMin="40" yMin="20" xMax="560" yMax="38">
<line xMin="40" yMin="20" xMax="560" yMax="38"><word xMin="40" yMin="20" xMax="100" yMax="38">Running</word><word xMin="110" yMin="20" xMax="160" yMax="38">head</word></line>
</block><block xMin="40" yMin="120" xMax="560" yMax="160"><line xMin="40" yMin="120" xMax="560" yMax="140"><word xMin="40" yMin="120" xMax="90" yMax="140">Trusted</word><word xMin="100" yMin="120" xMax="150" yMax="140">body</word></line></block></flow></page></doc></body></html>`)}
	analyzer, err := NewPopplerAnalyzer(AnalyzerConfig{
		PDFTextPath: "/usr/bin/pdftotext", MaximumPages: 10, MaximumItemsPerLocation: 20,
		MaximumXMLTokens: 1000, MaximumXMLDepth: 16, MaximumOutputBytes: 1 << 20,
		MaximumPageTextBytes: 1 << 19, MaximumItemTextBytes: 1 << 16, MaximumTextBytes: 1 << 20,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}

	document, err := analyzer.Analyze(context.Background(), "/tmp/source.pdf", "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	if runner.path != "/usr/bin/pdftotext" || strings.Join(runner.args, " ") != "-bbox-layout -enc UTF-8 /tmp/source.pdf -" {
		t.Fatalf("command = %q %q", runner.path, runner.args)
	}
	if len(document.Locations) != 1 || len(document.Locations[0].Items) != 2 {
		t.Fatalf("document = %#v", document)
	}
	header, body := document.Locations[0].Items[0], document.Locations[0].Items[1]
	if header.Label != "page_header" || header.ContentLayer != "furniture" || header.Text != "Running head" {
		t.Fatalf("header = %#v", header)
	}
	if body.Label != "paragraph" || body.ContentLayer != "body" || body.Text != "Trusted body" {
		t.Fatalf("body = %#v", body)
	}
}

func TestPopplerAnalyzerRejectsUntrustedOrUnboundedOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		config AnalyzerConfig
	}{
		{name: "no text", output: `<doc><page width="600" height="800"/></doc>`, config: validAnalyzerConfig()},
		{name: "too many pages", output: `<doc><page width="1" height="1"/><page width="1" height="1"/></doc>`, config: func() AnalyzerConfig { value := validAnalyzerConfig(); value.MaximumPages = 1; return value }()},
		{name: "unsafe directive", output: `<!DOCTYPE doc><doc/>`, config: validAnalyzerConfig()},
		{name: "invalid bounds", output: `<doc><page width="600" height="800"><flow><block><line xMin="20" yMin="20" xMax="10" yMax="30"><word>bad</word></line></block></flow></page></doc>`, config: validAnalyzerConfig()},
		{name: "bounds outside page", output: `<doc><page width="600" height="800"><flow><block><line xMin="20" yMin="20" xMax="700" yMax="30"><word>bad</word></line></block></flow></page></doc>`, config: validAnalyzerConfig()},
		{name: "unknown element", output: `<doc><page width="600" height="800"><script>bad</script></page></doc>`, config: validAnalyzerConfig()},
		{name: "unknown namespace", output: `<doc xmlns="urn:untrusted"><page width="600" height="800"/></doc>`, config: validAnalyzerConfig()},
		{name: "text outside word", output: `<doc><page width="600" height="800">bad</page></doc>`, config: validAnalyzerConfig()},
		{name: "comment", output: `<doc><!-- hidden --><page width="600" height="800"/></doc>`, config: validAnalyzerConfig()},
		{name: "item text limit", output: `<doc><page width="600" height="800"><flow><block><line xMin="20" yMin="20" xMax="30" yMax="30"><word>too-long</word></line></block></flow></page></doc>`, config: func() AnalyzerConfig { value := validAnalyzerConfig(); value.MaximumItemTextBytes = 3; return value }()},
		{name: "token limit", output: `<doc><page width="600" height="800"><flow><block><line xMin="20" yMin="20" xMax="30" yMax="30"><word>text</word></line></block></flow></page></doc>`, config: func() AnalyzerConfig { value := validAnalyzerConfig(); value.MaximumXMLTokens = 5; return value }()},
		{name: "depth limit", output: `<doc><page width="600" height="800"><flow><block><line xMin="20" yMin="20" xMax="30" yMax="30"><word>text</word></line></block></flow></page></doc>`, config: func() AnalyzerConfig { value := validAnalyzerConfig(); value.MaximumXMLDepth = 3; return value }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			analyzer, err := NewPopplerAnalyzer(test.config, &layoutRunnerStub{output: []byte(test.output)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = analyzer.Analyze(context.Background(), "/tmp/source.pdf", "application/pdf"); err == nil {
				t.Fatal("unsafe output accepted")
			}
		})
	}
}

func TestPopplerAnalyzerRejectsInvalidConfigurationAndMediaType(t *testing.T) {
	if _, err := NewPopplerAnalyzer(AnalyzerConfig{}, &layoutRunnerStub{}); err == nil {
		t.Fatal("invalid configuration accepted")
	}
	analyzer, err := NewPopplerAnalyzer(validAnalyzerConfig(), &layoutRunnerStub{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = analyzer.Analyze(context.Background(), "/tmp/source.txt", "text/plain")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func validAnalyzerConfig() AnalyzerConfig {
	return AnalyzerConfig{
		PDFTextPath: "/usr/bin/pdftotext", MaximumPages: 10, MaximumItemsPerLocation: 20,
		MaximumXMLTokens: 1000, MaximumXMLDepth: 16, MaximumOutputBytes: 1 << 20,
		MaximumPageTextBytes: 1 << 19, MaximumItemTextBytes: 1 << 16, MaximumTextBytes: 1 << 20,
	}
}
