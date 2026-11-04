package types_test

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/craggy-io/script-services/types"
)

const declarations = `package api

// DecompileIn is what apk.decompile takes.
type DecompileIn struct {
	Artifact string ` + "`json:\"artifact\" jsonschema:\"artifact id\"`" + `
	Depth    *int   ` + "`json:\"depth,omitempty\"`" + ` // how deep to go
	Level    Level  ` + "`json:\"level\"`" + `
	Tags     []string ` + "`json:\"tags,omitempty\"`" + `
}

// Level is a decompiler effort level.
// enum: fast, full
type Level string
`

// A Go-syntax declaration file becomes JSON Schema: closed objects, required
// unless pointer/omitempty/omitzero, enums from doc comments.
func ExamplePackage_Schema() {
	pkg, err := types.Parse("types.go", []byte(declarations))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(pkg.Name, pkg.Names())
	s, err := pkg.Schema("DecompileIn")
	if err != nil {
		fmt.Println(err)
		return
	}
	out, _ := json.MarshalIndent(s, "", "  ")
	fmt.Println(string(out))
	// Output:
	// api [DecompileIn Level]
	// {
	//   "type": "object",
	//   "properties": {
	//     "artifact": {
	//       "type": "string",
	//       "description": "artifact id"
	//     },
	//     "depth": {
	//       "type": [
	//         "null",
	//         "integer"
	//       ],
	//       "description": "how deep to go"
	//     },
	//     "level": {
	//       "type": "string",
	//       "description": "Level is a decompiler effort level.",
	//       "enum": [
	//         "fast",
	//         "full"
	//       ]
	//     },
	//     "tags": {
	//       "type": [
	//         "null",
	//         "array"
	//       ],
	//       "items": {
	//         "type": "string"
	//       }
	//     }
	//   },
	//   "description": "DecompileIn is what apk.decompile takes.",
	//   "required": [
	//     "artifact",
	//     "level"
	//   ],
	//   "additionalProperties": false
	// }
}

// Problems inside a type surface from Schema as positioned *types.Error
// values.
func ExampleError() {
	pkg, err := types.Parse("types.go", []byte("package api\n\ntype Node struct {\n\tKids []Node `json:\"kids\"`\n}\n"))
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = pkg.Schema("Node")
	var te *types.Error
	if errors.As(err, &te) {
		fmt.Println(te.Pos.Line, te.Msg)
	}
	fmt.Println(err)
	// Output:
	// 4 recursive type Node (Node -> Node)
	// types.go:4:9: recursive type Node (Node -> Node)
}
