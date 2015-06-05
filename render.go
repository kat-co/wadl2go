package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"
)

// Render the method.
func Render(writer io.Writer, packageName string, renderMethod func(io.Writer, *WADLMethod) error, methods ...*WADLMethod) error {

	fmt.Fprintf(writer, "package %s", packageName)

	// We need a function to make request.
	fmt.Fprintln(writer, "\n\ntype RequestHandlerFn func(*http.Request) (*http.Response, error)")
	for _, method := range methods {
		if err := renderMethod(writer, method); err != nil {
			return err
		}
	}
	return nil
}

// RenderMethodWithBulkTypes will render the specified method to the writer.
func RenderMethodWithBulkTypes(writer io.Writer, method *WADLMethod) error {
	const funBodyTmpl = `

{{if .Documentation}}{{renderDocumentation .Documentation}}{{end}}
func {{.FunName}}(request RequestHandlerFn, args {{.ArgType}}) ({{if .ResponseType}}*{{.ResponseType}},{{end}} error) {

	argsAsJson, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}

	url := "{{.Url}}"
	{{.ReplaceTemplateVarsCode}}

	var req *http.Request
	if string(argsAsJson) != "{}" {
		req, err = http.NewRequest("{{.MethodType}}", url, bytes.NewBuffer(argsAsJson))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest("{{.MethodType}}", url, nil)
		if err != nil {
			return nil, err
		}
	}

	{{if .ReplaceQueryVarsCode}}
		query := req.URL.Query()
		{{.ReplaceQueryVarsCode}}
		req.URL.RawQuery = query.Encode()
	{{end}}

	resp, err := request(req)
	if err != nil {
		return nil, err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	{{if .AcceptableStatusCodesCsv}}
	switch resp.StatusCode {
	default:
		return nil, fmt.Errorf("invalid status (%d): %s", resp.StatusCode, body)
	case {{.AcceptableStatusCodesCsv}}:
		break;
	}
	{{end}}

	var results {{.ResponseType}}
	json.Unmarshal(body, &results)
	{{/* TODO(katco-): Don't ignore error here; look at num of items in response collection */}}
	return &results, nil
}`

	methName := renderIdentifiers(method.Name, false)
	debug.Printf("methName: %s\n", methName)

	RenderParameterType(writer, methName, method.Arguments)
	returnStruct := exampleToStruct(method.ResultsExample, renderMethodResultsName(methName))
	if returnStruct != "" {
		fmt.Fprintf(writer, "\n\n%s\n", returnStruct)
	} else {
		// We Always want to return something.
		RenderResultsType(writer, methName, method.Results)
	}

	const templateVarReplaceTmpl = `
url = strings.Replace(url, "%7B<!.Name!>%7D", args.<!renderIdentifiers .Name true!>, -1)`
	const queryVarReplaceTmpl = `
query.Add("{{.Name}}", fmt.Sprintf("%v", args.{{renderIdentifiers .Name true}}))`

	var replaceTemplateVarsCode bytes.Buffer
	var replaceQueryVarsCode bytes.Buffer
	var bodyParams []*WADLVariable
	for _, param := range method.Arguments {
		debug.Printf("param type: %s", param.RequestType)
		switch param.RequestType {
		case "template":
			var codeSnippet bytes.Buffer
			if err := template.Must(template.New("").Funcs(template.FuncMap{
				"renderIdentifiers": renderIdentifiers,
			}).Delims("<!", "!>").Parse(templateVarReplaceTmpl)).Execute(&codeSnippet, param); err != nil {
				panic(err)
			}

			if _, err := replaceTemplateVarsCode.Write(codeSnippet.Bytes()); err != nil {
				panic(err)
			}

		case "query":
			var codeSnippet bytes.Buffer
			if err := template.Must(template.New("").Funcs(template.FuncMap{
				"renderIdentifiers": renderIdentifiers,
			}).Parse(queryVarReplaceTmpl)).Execute(&codeSnippet, param); err != nil {
				panic(err)
			}

			if _, err := replaceQueryVarsCode.Write(codeSnippet.Bytes()); err != nil {
				panic(err)
			}
		case "plain":
			bodyParams = append(bodyParams, param)
		}
	}

	var funBody bytes.Buffer
	if err := template.Must(template.New("").Funcs(template.FuncMap{
		"renderDocumentation": renderDocumentation,
	}).Parse(funBodyTmpl)).Execute(&funBody, &struct {
		Documentation            string
		FunName                  string
		ArgType                  string
		ResponseType             string
		MethodType               string
		URL                      string
		ReplaceTemplateVarsCode  string
		ReplaceQueryVarsCode     string
		AcceptableStatusCodesCsv string
	}{
		method.Documentation,
		methName,
		renderMethodParamName(methName),
		renderMethodResultsName(methName),
		method.Type,
		method.URL,
		replaceTemplateVarsCode.String(),
		replaceQueryVarsCode.String(),
		strings.Join(method.AcceptableStatus, ","),
	}); err != nil {
		panic(err)
	}

	fmt.Fprint(writer, funBody.String())
	return nil
}

func exampleToStruct(example string, typeName string) string {
	cmd := exec.Command("gojson", "-name", typeName)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		panic(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}

	if err := cmd.Start(); err != nil {
		panic(err)
	}
	if _, err := stdin.Write([]byte(example)); err != nil {
		panic(err.Error())
	}
	stdin.Close()

	stdoutReader := bufio.NewReader(stdout)
	stdoutReader.ReadLine()
	stdoutReader.ReadLine()

	output, err := ioutil.ReadAll(stdoutReader)
	if err != nil {
		panic(err)
	}

	cmd.Wait()

	return string(output)
}

// RenderParameterType builds the variable code.
func RenderParameterType(writer io.Writer, methName string, params []*WADLVariable) {
	renderVariableCollection(writer, methName, params, renderMethodParamName)
}

// RenderResultsType builds the results type code.
func RenderResultsType(writer io.Writer, methName string, params []*WADLVariable) {
	renderVariableCollection(writer, methName, params, renderMethodResultsName)
}

func renderDocumentation(doc string) string {
	var docBlock bytes.Buffer
	r := bufio.NewReader(strings.NewReader(doc))
	for lineLen := 0; ; {
		lineBytes, _, err := r.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalf("error reading documentation: %s", err)
		}

		lineLen += len(lineBytes)

		fmt.Fprintf(&docBlock, " %s", strings.TrimSpace(string(lineBytes)))
	}

	// Start scrubbing.
	line := string(docBlock.String())
	line = regexp.MustCompile("<para[^>]*>").ReplaceAllString(line, "\n// ")
	line = regexp.MustCompile("</para>").ReplaceAllString(line, "")
	line = regexp.MustCompile("<code[^>]*>").ReplaceAllString(line, "")
	line = regexp.MustCompile("</code>").ReplaceAllString(line, "")
	line = regexp.MustCompile("<itemizedlist[^>]*>").ReplaceAllString(line, "\n// ")
	line = regexp.MustCompile("</itemizedlist>").ReplaceAllString(line, ":")
	line = regexp.MustCompile("<listitem[^>]*>").ReplaceAllString(line, "")
	line = regexp.MustCompile("</listitem>").ReplaceAllString(line, "")
	line = regexp.MustCompile("//\\s*$").ReplaceAllString(line, "")

	debug.Printf("New doc:\n%s", line) //docBlock.String())
	return "// " + line                //docBlock.String()
}

func renderVariableCollection(writer io.Writer, methName string, params []*WADLVariable, renderCollectionName func(string) string) {
	const collectionType = `

type {{.CollectionName}} struct {
	{{range .Variables}}
		{{if .Required}}// {{renderIdentifiers .Name true}} is required.{{end}}
		{{if .Documentation}}{{renderDocumentation .Documentation}}{{end}}
		{{renderIdentifiers .Name true}} {{renderType .Type}} ` + "`json:\"{{if eq .RequestType \"plain\"}}{{.Name}}{{if not .Required}},omitempty{{end}}{{else}}-{{end}}\"`" + `
	{{end}}
}`

	// Create sub-types for variables with embedded objects.
	for _, p := range params {
		if len(p.EmbeddedVar) <= 0 {
			continue
		}
		typeName := renderIdentifiers(methName+caseFirstChar(p.Name, true), true)
		renderVariableCollection(writer, typeName, p.EmbeddedVar, renderCollectionName)
		p.Type = renderCollectionName(typeName)
	}

	var typeBody bytes.Buffer
	if err := template.Must(template.New("collection").Funcs(template.FuncMap{
		"renderIdentifiers":   renderIdentifiers,
		"renderType":          renderType,
		"renderDocumentation": renderDocumentation,
	}).Parse(collectionType)).Execute(&typeBody, struct {
		CollectionName string
		Variables      []*WADLVariable
		FormatName     func(string, bool) string
	}{
		CollectionName: renderCollectionName(methName),
		Variables:      params,
		FormatName:     renderIdentifiers,
	}); err != nil {
		panic(err)
	}
	fmt.Fprintf(writer, typeBody.String())
}

func renderIdentifiers(name string, isPublic bool) string {
	for _, camelCaseSentinel := range []string{"_", "-"} {
		for {
			sntlIdx := strings.Index(name, camelCaseSentinel)
			if sntlIdx < 0 {
				break
			}
			name = name[:sntlIdx] + caseFirstChar(name[sntlIdx+1:], true)
		}
	}

	return caseFirstChar(name, isPublic)
}

func renderMethodParamName(methName string) string {
	return renderIdentifiers(fmt.Sprintf("%sParams", methName), true)
}

func renderMethodResultsName(methName string) string {
	return renderIdentifiers(fmt.Sprintf("%sResults", methName), true)
}

func renderType(wadlType string) string {
	switch strings.ToLower(wadlType) {
	default:
		log.Printf("WARNING: unknown WADL type: %s", wadlType)
		return wadlType
	case "object":
		// TODO(katco-): Correctly reference the auto-generated structure type.
		return "interface{}"
	case "xsd:datetime":
		return "time.Time"
	case "string", "xsd:string", "csapi:uuid", "csapi:string":
		return "string"
	case "xsd:int", "integer":
		return "int"
	case "xsd:boolean", "boolean":
		return "bool"
	}
}

func caseFirstChar(str string, toUpper bool) string {
	r, n := utf8.DecodeRuneInString(str)
	if toUpper {
		str = string(unicode.ToUpper(r)) + str[n:]
	} else {
		str = string(unicode.ToLower(r)) + str[n:]
	}
	return str
}
