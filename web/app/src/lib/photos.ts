// Shared photo/attachment helpers. A "photo" row can be an IMAGE (jpg/png/webp) or a
// DOCUMENT attachment (pdf, om-9o4n.2). A document rides the same photos table but has NO
// thumb/display derivative — the server skipped the imaging pipeline at ingest — so any UI
// that would render a photo as an <img> must first branch on isDocumentExt and render a
// document affordance (a card/tile with an open/download link) instead. Rendering a doc as
// an <img> only ever shows a broken image (its display/thumb variants 404 by design).
//
// This is the SINGLE frontend source of the document ext set — PhotoGallery (the drawer) and
// TrophyFeed (the Insights hero) both import it, so a new document type is added in ONE place.
// Mirrors the server's closed sets (imaging.docExts + model.Photo.Validate).
const DOC_EXTS = new Set(['pdf'])

// isDocumentExt reports whether an ext names a document attachment (a PDF today) rather than a
// decodable image. Case-folded because ext is stored/compared lowercase.
export function isDocumentExt(ext: string | undefined | null): boolean {
  return DOC_EXTS.has((ext ?? '').toLowerCase())
}
