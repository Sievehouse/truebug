// Package treesitter holds the syntactic layer: fast, build-free parsing that
// works even when a repository does not compile.
//
// It exists for two reasons. Chunk boundaries for embeddings should fall on
// function boundaries rather than arbitrary line counts, and a repository
// whose build fails should still produce a degraded report rather than
// nothing.
//
// No grammar is wired up yet; these are the types the parser will fill.
package treesitter

// Chunk is a syntactic unit worth embedding or showing to a model.
type Chunk struct {
    ID       string `json:"id"`
    File     string `json:"file"`
    Language string `json:"language"`
    Kind     string `json:"kind"`
    Name     string `json:"name"`

    LineStart int `json:"line_start"`
    LineEnd   int `json:"line_end"`

    Text string `json:"text"`

    // Hash is the content hash. Chunking is cached by it, so an unchanged file
    // is never re-parsed on any branch.
    Hash string `json:"hash"`
}

const (
    ChunkFunction = "function"
    ChunkMethod   = "method"
    ChunkType     = "type"
    ChunkComment  = "comment"
    ChunkImports  = "imports"
)

// Parser extracts chunks from a file without building it.
type Parser interface {
    Languages() []string
    Chunks(path string, src []byte) ([]Chunk, error)
}
