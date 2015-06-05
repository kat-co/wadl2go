package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/kat-co/wadl2go/wadl"
	"github.com/katco-/vala"
)

var (
	debug *log.Logger
)

// WADLEntryDoc is entry point to a wadl document
type WADLEntryDoc struct {
	XMLName xml.Name `xml:"application"`
	wadl.TxsdApplication
}

func main() {

	showDebug := flag.Bool("debug", false, "Controls debug log messages")
	wadlFilePath := flag.String("wadl-file", "", "Specifies which file to parse")
	toFile := flag.String("to-file", "", "Specifies the destination file")
	// TODO(katco-): Set default value to derived value from to-file PWD.
	packageName := flag.String("package-name", "main", "Specifies the package the generated file will be under.")
	userBaseURL := flag.String("base-url", "", "Specifies a replacement for the given base URL.")
	flag.Parse()

	var debugBuff io.Writer
	// TODO(katco-): Switch on flag
	if *showDebug {
		debugBuff = os.Stderr
	} else {
		debugBuff = ioutil.Discard
	}
	debug = log.New(debugBuff, "DEBUG: ", 0)

	// Make sure we have a well-formed method.
	if err := vala.BeginValidation().Validate(
		vala.StringNotEmpty(*wadlFilePath, "wadl-file"),
		vala.StringNotEmpty(*toFile, "to-file"),
		vala.StringNotEmpty(*packageName, "package-name"),
	).Check(); err != nil {
		flag.Usage()
		os.Exit(0)
	}

	contents, err := ioutil.ReadFile(*wadlFilePath)
	if err != nil {
		panic(err)
	}

	var rawDoc WADLEntryDoc
	if err := xml.Unmarshal(contents, &rawDoc); err != nil && err != io.EOF {
		log.Fatal(err)
	}

	if len(wadl.WalkErrors) > 0 {
		panic(wadl.WalkErrors)
	}

	structuredDoc := WADLDoc{Methods: make(map[string]*WADLMethod)}

	// Pull type information from the grammars.
	var grammarTypes []*WADLVariable
	for _, grammar := range rawDoc.Grammars.Includes {
		fileType := path.Ext(string(grammar.Href))
		switch fileType {
		default:
			log.Printf("WARNING: skipping unsupported grammar type: %v", fileType)
		case ".json":
			rawSchema, err := readJSONSchemaFile(path.Join(path.Dir(*wadlFilePath), string(grammar.Href)))
			if err != nil {
				log.Fatalf("could not read JSON schema: %v", err)
			}
			grammarTypes = append(grammarTypes, rawJSONSchemaParamToParam(rawSchema)...)
		}
	}

	// Build methods.
	for _, rawMethod := range rawDoc.Methods {
		debug.Printf("rawMethod: %s", rawMethod.Id)
		method := &WADLMethod{
			Documentation: rawDocsToDoc(rawMethod.Docs),
			Name:          string(rawMethod.Id),
			Type:          string(rawMethod.Name),
		}
		if rawMethod.Request != nil {
			debug.Println("request found")
			method.Arguments = append(method.Arguments, rawParamToVariable(rawMethod.Request.Params)...)
			for _, rawRep := range rawMethod.Request.Representations {
				// HACK(katco-): Care about more than JSON representations
				if string(rawRep.MediaType) != "application/json" {
					log.Printf("INFO: skipping request representation: %s", rawRep.MediaType)
					continue
				}

				// Check for parameters defined in the grammar.
				// HACK(katco-): We're specifically checking the json:ref attrbite for Openstack.
				debug.Printf("jsonref: %s", rawRep.JsonRef)
				if grammarRef := rawRep.JsonRef.String(); grammarRef != "" {
					// We know that any variables we might be trying
					// to reference will be at the top-level, and not
					// embedded.
					for _, grammarVar := range grammarTypes {
						if grammarVar.URI != grammarRef {
							continue
						}

						method.Arguments = append(method.Arguments, grammarVar)
					}
				}

				method.Arguments = append(method.Arguments, rawParamToVariable(rawRep.Params)...)
			}
		}
		for _, rawResponse := range rawMethod.Responses {
			method.Results = append(method.Results, rawParamToVariable(rawResponse.Params)...)
			method.AcceptableStatus = append(
				method.AcceptableStatus,
				strings.Split(string(rawResponse.Status), " ")...,
			)

			for _, rawRep := range rawResponse.Representations {
				// HACK(katco-): Care about more than JSON representations
				if string(rawRep.MediaType) != "application/json" {
					log.Printf("INFO: skipping response representation: %s", rawRep.MediaType)
					continue
				}

				// HACK(katco-): Don't assume <= 1 doc elements.
				example, err := dereferenceExampleFile(path.Dir(*wadlFilePath), rawRep.Docs[0].XsdGoPkgCDATA)
				if err != nil {
					log.Fatal(err)
				}

				debug.Printf("example: %s", example)

				method.ResultsExample = example
				method.Results = append(method.Results, rawParamToVariable(rawRep.Params)...)
				break
			}
		}
		structuredDoc.Methods[method.Name] = method
	}

	for _, resources := range rawDoc.Resourceses {
		baseURL := *userBaseURL
		if baseURL == "" {
			baseURL = string(resources.Base)
		}

		parsedBaseURL, err := url.Parse(baseURL)
		if err != nil {
			log.Fatalf("could not determine the base URL: %s", err)
		}
		debug.Println("base: " + parsedBaseURL.String())
		recurseResources(structuredDoc.Methods, *parsedBaseURL, nil, resources.Resources)
	}

	var methods []*WADLMethod
	for _, m := range structuredDoc.Methods {
		methods = append(methods, m)
	}

	var file bytes.Buffer
	Render(&file, *packageName, RenderMethodWithBulkTypes, methods...)

	ioutil.WriteFile(*toFile, file.Bytes(), 0640)
}

func readJSONSchemaFile(filePath string) (map[string]interface{}, error) {
	body, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}

	return m, nil
}

func rawJSONSchemaParamToParam(rawParams map[string]interface{}) (params []*WADLVariable) {

	// First discover all variables.
	if properties, ok := rawParams["properties"].(map[string]interface{}); ok {
		for varName, varAttrs := range properties {
			newParam := &WADLVariable{Name: varName, RequestType: "plain"}

			for attrName, attr := range varAttrs.(map[string]interface{}) {
				switch strings.ToLower(attrName) {
				case "id":
					newParam.URI = attr.(string)
				case "type":
					newParam.Type = attr.(string)
				case "properties":
					debug.Printf("JSON SCHEMA: ATTR: %v", varAttrs)
					newParam.EmbeddedVar = rawJSONSchemaParamToParam(varAttrs.(map[string]interface{}))
				case "documentation":
					newParam.Documentation = attr.(string)
				}
			}

			params = append(params, newParam)
		}
	}

	// Then flag the required ones.
	if requiredProps, ok := rawParams["required"].([]interface{}); ok {
		for _, requiredParamName := range requiredProps {
			found := false
			for _, knownParam := range params {
				if knownParam.Name != requiredParamName {
					continue
				}
				found = true
				knownParam.Required = true
			}
			if !found {
				log.Printf("WARNING: Unknown variable (%s) was declared as required", requiredParamName)
			}
		}
	}

	debug.Printf("JSON SCHEMA: PARAMS: %v", params)
	return params
}

// WADLDoc has a list of wadl methods
type WADLDoc struct {
	Methods map[string]*WADLMethod
}

// WADLMethod are the methods on a service.
type WADLMethod struct {
	Documentation string
	Name          string
	Type          string
	URL           string
	Arguments     []*WADLVariable
	Results       []*WADLVariable
	// TODO(katco-): Track Results element attribute for dereferencing types.
	ResultsExample   string
	AcceptableStatus []string
}

// WADLVariable is the variable used in a method.
type WADLVariable struct {
	URI           string
	Documentation string
	Name          string
	Type          string
	RequestType   string
	Required      bool
	Path          string
	EmbeddedVar   []*WADLVariable
}

func dereferenceExampleFile(basePath, innerXML string) (string, error) {

	debug.Printf("inner XML: %s", innerXML)

	type DocElem struct {
		Href string `xml:"href,attr"`
	}
	var elem DocElem
	if err := xml.NewDecoder(strings.NewReader(innerXML)).Decode(&elem); err != nil {
		return "", err
	}

	debug.Printf("reading file: %s", elem.Href)

	bytes, err := ioutil.ReadFile(path.Join(basePath, elem.Href))
	return string(bytes), err
}

func recurseResources(
	methods map[string]*WADLMethod,
	base url.URL, // Copy so we can modify it freely.
	params []*WADLVariable,
	resources []*wadl.TxsdResource,
) {
	for _, resource := range resources {
		baseCopy := base
		baseCopy.Path = filepath.Join(baseCopy.Path, string(resource.Path))
		params = append(params, rawParamToVariable(resource.Params)...)

		debug.Printf("url for %s: %s", resource.Id, baseCopy)

		recurseResources(methods, baseCopy, params, resource.Resources)

		for _, rawMethod := range resource.Methods {
			method, ok := methods[string(rawMethod.Href)[1:]]
			if !ok {
				log.Printf("WARNING: referenced method %s was not found", rawMethod)
				continue
			}
			method.URL = baseCopy.String()
			method.Arguments = append(method.Arguments, params...)
		}
	}
}

func rawParamToVariable(params []*wadl.TxsdParam) (vars []*WADLVariable) {
	for _, rawParam := range params {
		vars = append(vars, &WADLVariable{
			Documentation: rawDocsToDoc(rawParam.Docs),
			Name:          string(rawParam.Name),
			Type:          string(rawParam.Type),
			Required:      bool(rawParam.Required),
			RequestType:   string(rawParam.Style),
			Path:          string(rawParam.Path),
		})
	}
	return vars
}

func rawDocsToDoc(docs []*wadl.TxsdDoc) string {
	var comment bytes.Buffer
	for _, d := range docs {
		debug.Printf("KT: raw doc: %v", d)
		fmt.Fprintf(&comment, "%s\n", strings.TrimSpace(d.XsdGoPkgCDATA))
	}
	debug.Println("Documentation: " + strings.TrimSpace(comment.String()))
	return strings.TrimSpace(comment.String())
}
