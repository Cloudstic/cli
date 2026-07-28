package source

// TopoSortFolderChanges orders folder upsert changes so that every parent
// appears before its children, using the raw source parent IDs in Meta.Parents.
//
// Incremental sources need this because Source implementations must emit
// parents before children (see Source.Walk), but a change feed reports
// entries in modification order, which carries no such guarantee.
//
// Changes whose parent is not itself in the batch keep their relative order;
// only entries that constrain each other are reordered.
func TopoSortFolderChanges(changes []FileChange) []FileChange {
	byID := make(map[string]int, len(changes))
	for i, fc := range changes {
		byID[fc.Meta.FileID] = i
	}

	visited := make(map[string]bool, len(changes))
	sorted := make([]FileChange, 0, len(changes))

	var visit func(idx int)
	visit = func(idx int) {
		fc := changes[idx]
		if visited[fc.Meta.FileID] {
			return
		}
		visited[fc.Meta.FileID] = true
		for _, pid := range fc.Meta.Parents {
			if pidx, ok := byID[pid]; ok {
				visit(pidx)
			}
		}
		sorted = append(sorted, fc)
	}

	for i := range changes {
		visit(i)
	}
	return sorted
}
