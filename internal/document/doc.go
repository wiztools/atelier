// Package document is the document editor's pure half: the Fountain
// screenplay parser, the line/word document diff behind the compare view, and
// the derived exports — a screenplay or markdown document laid out as a PDF on
// US Letter or A4, a screenplay as a Final Draft .fdx, and tabular reports
// (report_export.go — the usage report's PDF) on the same page writer. Nothing here
// touches conversations, artifacts, or the UI; the app's bindings
// (SaveDocumentExport, DefaultDocumentPaperSize, ResolveDocumentCompare,
// SaveUsageReportExport) read the files and hand the bytes in.
//
// fountain.go mirrors frontend/src/editor/fountainModel.ts rule for rule, so
// the editor's preview and the printed page cannot drift.
package document
