package main

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"text/template"

	"github.com/algorand/conduit/conduit/plugins/processors/filterprocessor/gen/internal"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"
)

// ignoreTags are things that we specifically want to exclude from the output.
var ignoreTags = map[string]bool{
	// this is a constant
	"txn.gh": true,
	// no point to filtering on a lease
	"txn.lx": true,
	// no point to filter on signatures
	"sig":              true,
	"msig.subsig":      true,
	"lsig.sig":         true,
	"lsig.arg":         true,
	"lsig.l":           true,
	"lsig.msig.subsig": true,
	// no point in filtering on keys
	"txn.votekey": true,
	"txn.selkey":  true,
	"txn.sprfkey": true,
	// no point in filtering on state proof things?
	"txn.sp.c":       true,
	"txn.sp.S.pth":   true,
	"txn.sp.S.hsh":   true,
	"txn.sp.S.hsh.t": true,
	"txn.sp.P.pth":   true,
	"txn.sp.P.hsh":   true,
	"txn.sp.P.hsh.t": true,
	"txn.sp.r":       true,
	"txn.sp.pr":      true,
	"txn.spmsg.b":    true,
	"txn.spmsg.v":    true,
	// inner transactions are handled differently
	"dt.itx": true,
	// TODO: this can be removed if the sub fields are supported
	"dt": true,
	// TODO: support map types?
	"dt.gd": true,
	"dt.ld": true,
	// TODO: support slice/array types?
	"txn.apaa": true,
	"txn.apat": true,
	"txn.apfa": true,
	"txn.apbx": true,
	"txn.apas": true,
	"txn.apap": true,
	"txn.apsu": true,
	"dt.lg":    true,
	"dt.sa":    true,
	// Filter out heartbeat fields that are not obvious how to filter upon
	"txn.hb": true,
}

func noCast(t reflect.StructField) bool {
	switch reflect.New(t.Type).Elem().Interface().(type) {
	case uint64:
		return true
	case int64:
		return true
	case string:
		return true
	}
	return false
}

func simpleCast(t reflect.StructField) string {
	switch reflect.New(t.Type).Elem().Interface().(type) {
	// unsigned
	case uint:
		return "uint64"
	case uint8:
		return "uint64"
	case uint16:
		return "uint64"
	case uint32:
		return "uint64"
	// signed
	case int:
		return "int64"
	case int8:
		return "int64"
	case int16:
		return "int64"
	case int32:
		return "int64" //
	// alias
	// SDK types
	case sdk.MicroAlgos:
		// SDK microalgo does not need ".Raw"
		return "uint64"
	case sdk.AssetIndex:
		return "uint64"
	case sdk.AppIndex:
		return "uint64"
	case sdk.Round:
		return "uint64"
	case sdk.OnCompletion:
		return "uint64"
	case sdk.StateProofType:
		return "uint64"
	case sdk.TxType:
		return "string"
	}
	return ""

}

func castParts(t reflect.StructField) (prefix, postfix string, err error) {
	// if no cast is needed... do not add a prefix / postfix.
	if noCast(t) {
		return
	}

	// for simple casts, get a simple type and do them all at once.
	if simple := simpleCast(t); simple != "" {
		prefix = fmt.Sprintf("%s(", simple)
		postfix = ")"
		return
	}

	// otherwise the cast is more complex, handle them case by case.

	encodeB64 := func() {
		prefix = "base64.StdEncoding.EncodeToString("
		postfix = "[:])"
	}

	switch v := reflect.New(t.Type).Elem().Interface().(type) {
	case bool:
		prefix = "fmt.Sprintf(\"%t\", "
		postfix = ")"
	// go-algorand-sdk types
	case sdk.Address:
		prefix = ""
		postfix = ".String()"
	case sdk.Digest:
		encodeB64()
	case []uint8: // note field
		encodeB64()
	case [32]uint8: // asset metadata, lease
		encodeB64()
	default:
		prefix = "NOT "
		postfix = " HANDLED"
		err = fmt.Errorf("failed to cast type: %T", v)
	}
	return
}

func getFields(theStruct interface{}, ignoreTags map[string]bool) (map[string]internal.StructField, []error) {
	output := make(map[string]internal.StructField)
	errors := recursiveTagFields(theStruct, ignoreTags, output, nil, nil)
	return output, errors
}

// recursiveTagFields recursively gets all field names in a struct
// Output will contain a key of the full tag along with the fully qualified struct
func recursiveTagFields(theStruct interface{}, ignoreTags map[string]bool, output map[string]internal.StructField, tagLevel []string, fieldLevel []string) []error {
	var errors []error
	rStruct := reflect.TypeOf(theStruct)
	numFields := rStruct.NumField()
	for i := 0; i < numFields; i++ {
		field := rStruct.Field(i)
		name := field.Name
		numOutputsBefore := len(output)

		// Lookup codec tag
		tagValue, foundTag := field.Tag.Lookup("codec")

		if field.Type.Kind() == reflect.Struct {
			var passedTagLevel []string
			if foundTag {
				passedTagLevel = append(tagLevel, tagValue)
			} else {
				passedTagLevel = tagLevel
			}
			errors = append(errors, recursiveTagFields(reflect.New(field.Type).Elem().Interface(), ignoreTags, output, passedTagLevel, append(fieldLevel, name))...)
		}

		// Add to output if there is a tag, and there were no subtags (i.e. this is a leaf)
		foundSubtag := numOutputsBefore < len(output)
		if foundTag && !foundSubtag {
			vals := strings.Split(tagValue, ",")
			// Get the first value (the one we care about)
			tagValue = vals[0]
			// If it is empty ignore it
			if tagValue == "" {
				continue
			}

			fullTag := strings.Join(append(tagLevel, tagValue), ".")
			if ignoreTags[fullTag] {
				continue
			}
			sf := internal.StructField{
				TagPath:   fullTag,
				FieldPath: strings.Join(append(fieldLevel, name), "."),
			}
			var err error
			sf.CastPrefix, sf.CastPost, err = castParts(field)
			if err != nil {
				errors = append(errors, fmt.Errorf("problem casting %s: %s", fullTag, err))
			}
			output[fullTag] = sf
		}
	}
	return errors
}

func writeFieldsToFile(filepath string, fields map[string]internal.StructField) error {
	fout, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer fout.Close()
	// sort the tags
	tags := make([]string, 0, len(fields))
	for k := range fields {
		tags = append(tags, k)
	}
	sort.Strings(tags)
	_, err = fout.WriteString(fmt.Sprintf("|%s|%s|\n", "filter tag", "transaction field"))
	if err != nil {
		return err
	}
	_, err = fout.WriteString("| -------- | ------- |\n")
	if err != nil {
		return err
	}
	for _, tag := range tags {
		_, err = fout.WriteString(fmt.Sprintf("|%s|%s|\n", tag, fields[tag].FieldPath))
		if err != nil {
			return err
		}
	}
	return err
}

const templateStr = `// Code generated via go generate. DO NOT EDIT.

package {{ .PackageName }}

import (
	"encoding/base64"
	"fmt"

	sdk "github.com/algorand/go-algorand-sdk/v2/types"
)

// LookupFieldByTag takes a tag and associated SignedTxnWithAD and returns the value
// referenced by the tag.  An error is returned if the tag does not exist
func LookupFieldByTag(tag string, input *sdk.SignedTxnWithAD) (interface{}, error) {
	switch tag {
{{ range .StructFields }}	case "{{ .TagPath }}":
		value := {{ ReturnValue . "input" }}
		return value, nil
{{ end }}	default:
		return nil, fmt.Errorf("unknown tag: %s", tag)
	}
}
`

// usage:
// go run generate.go packagename outputfile tagsfile
func main() {
	var packageName string
	var outputFilepath string
	var tagsFilepath string

	if len(os.Args) == 3 {
		packageName = os.Args[1]
		outputFilepath = os.Args[2]
	} else if len(os.Args) == 4 {
		packageName = os.Args[1]
		outputFilepath = os.Args[2]
		tagsFilepath = os.Args[3]
	}

	if packageName == "" {
		packageName = "NULL"
	}

	// Initialize template, no point to continue if there is a problem with it.
	ut, err := template.
		New("LookupFieldByTag").
		Funcs(map[string]interface{}{
			"ReturnValue": internal.ReturnValue,
		}).
		Parse(templateStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse template string: %s", err)
		os.Exit(1)
	}

	// Process fields.
	fields, errors := getFields(sdk.SignedTxnWithAD{}, ignoreTags)
	if len(errors) != 0 {
		fmt.Fprintln(os.Stderr, "Errors returned while getting struct fields:")
		for _, err := range errors {
			fmt.Fprintf(os.Stderr, "  * %s\n", err)
		}
		os.Exit(1)
	}

	// Setup writer to stdout or file.
	var outputWriter io.Writer
	if outputFilepath == "" {
		outputWriter = os.Stdout
	} else {
		fout, err := os.Create(outputFilepath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot open %s for writing: %v\n", outputFilepath, err)
			os.Exit(1)
		}
		defer fout.Close()
		outputWriter = fout
	}

	// Prepare template inputs.
	data := struct {
		StructFields map[string]internal.StructField
		PackageName  string
	}{
		StructFields: fields,
		PackageName:  packageName,
	}

	// Process template and write results.
	err = ut.Execute(outputWriter, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Template execute failure: %s", err)
		os.Exit(1)
	}

	// write filter tags to a file
	if tagsFilepath != "" {
		err = writeFieldsToFile(tagsFilepath, fields)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error while creating file %s, err: %v\n", tagsFilepath, err)
		}
	}
}
