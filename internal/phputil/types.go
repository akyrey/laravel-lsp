package phputil

import "strings"

// primitiveTypes are PHP built-in and pseudo types that never name a class.
var primitiveTypes = map[string]bool{
	"string": true, "int": true, "integer": true, "float": true, "double": true,
	"bool": true, "boolean": true, "array": true, "null": true, "void": true,
	"mixed": true, "never": true, "object": true, "iterable": true, "callable": true,
	"false": true, "true": true, "self": true, "static": true, "parent": true,
	"resource": true,
}

// IsPrimitiveType reports whether a type name (case-insensitive) is a PHP
// built-in or pseudo type rather than a class reference.
func IsPrimitiveType(name string) bool {
	return primitiveTypes[strings.ToLower(name)]
}
