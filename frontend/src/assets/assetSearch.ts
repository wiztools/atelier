// The assets panel's search: one query box matching an asset's id or its
// user-set alias (asset_aliases.go on the Go side). Pure/module-level for
// unit testing — the panel feeds it the rows it already lists, so the search
// never re-derives anything. The parameter is the structural shape the row
// needs (main.ConversationAsset satisfies it), keeping this module free of
// the generated bindings.

// assetMatchesQuery reports whether an asset matches a search query:
// case-insensitive substring over the id and the alias — the two names the
// panel row shows. A bare prefix ("img_ab12") and a mid-alias fragment
// ("shot") both hit. An empty or whitespace query matches everything (the
// unfiltered list).
function assetMatchesQuery(asset: {id: string; alias?: string}, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) {
    return true;
  }
  if (asset.id.toLowerCase().includes(q)) {
    return true;
  }
  return asset.alias !== undefined && asset.alias.toLowerCase().includes(q);
}

export {assetMatchesQuery};
