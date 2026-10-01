// Package knowledge is the pure-Go retrieval store behind an agent profile's
// documents and a project's scanner output.
//
// Three pieces, all in process, with no cgo, no model file and no service:
//
//   - a pipeline that turns text into chunks and chunks into hashed vectors
//     (signed feature hashing of words, word pairs and character trigrams,
//     tf-idf weighted, unit length);
//   - an index that holds the chunks and answers a query by cosine
//     similarity through an inverted index, and that is written to disk with a
//     magic, a version and a SHA-256 trailer;
//   - a Set that merges several indexes (a profile's, the project's, a
//     session's) into one ranked answer for the file tools.
//
// Trust. Chunk text is arbitrary document text. It is sanitised and run
// through a Gate (the security classifier, in practice) once, when it is
// ingested, and a chunk the Gate does not admit is never stored. A search is
// therefore a lookup over already admitted text and calls nothing. The index
// holds chunk text, so it lives in the state directory, which the OS sandbox
// hides from every command: a model reaches it only through the file tools.
package knowledge
