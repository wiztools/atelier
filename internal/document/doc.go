// Package document is the document editor's pure half: the Fountain
// screenplay parser, the line/word document diff behind the compare view, and
// the derived exports — a screenplay or markdown document laid out as a PDF on
// US Letter or A4, and a screenplay as a Final Draft .fdx. Nothing here
// touches conversations, artifacts, or the UI; the app's bindings
// (SaveDocumentExport, DefaultDocumentPaperSize, ResolveDocumentCompare) read
// the files and hand the bytes in.
//
// fountain.go mirrors frontend/src/editor/fountainModel.ts rule for rule, so
// the editor's preview and the printed page cannot drift.
package document
