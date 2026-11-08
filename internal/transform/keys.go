package transform

type functionKey struct {
	name, systemName, filename int32
	startLine                  int64
}

type mappingKey struct {
	start, limit, offset uint64
	filename             int32
	attrs                string
}

type attrKey struct {
	key, unit int32
	kind      byte
	value     string
}

const (
	kindString byte = iota + 1
	kindInt
	kindBool
	kindStrings
	kindInts
)
