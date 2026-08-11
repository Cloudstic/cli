package objkey

// Set is a set of object keys, held compactly.
//
// It exists for the two structures sized by the repository rather than by the
// work in front of them: prune's reachable set, which the sweep consults for
// every object it lists, and check's verified set, which stops the walk
// revisiting shared objects. Both were map[string]bool — a pointer per entry for
// the garbage collector to trace, and 73 bytes of hex to carry 32 bytes of hash.
//
// # Totality
//
// Set is total over strings: Add accepts any key, and Has then reports it.
// Nothing is dropped, and nothing collides.
//
// That is a correctness property, not a nicety. docs/compatibility.md is
// normative that a garbage collector must never read "cannot represent" as "not
// referenced" — a key missing from prune's reachable set is an object the sweep
// believes is garbage and deletes. So a key that does not encode is kept
// verbatim in a string-keyed fallback rather than refused, and the two maps
// cannot disagree with each other:
//
//   - Encode is a pure function of the key, so a key encodes on every call or on
//     none. Add and Has therefore always consult the same map for a given key.
//   - Encode is injective (see its doc), so two distinct keys never share a Key,
//     and a compact entry can only be reported for the key that added it.
//
// The fallback is expected to hold nothing at all in a modern repository — every
// swept namespace is content-addressed — so keeping it exact costs nothing.
type Set struct {
	compact  map[Key]struct{}
	fallback map[string]struct{}
}

// NewSet returns an empty Set.
func NewSet() *Set {
	return &Set{
		compact:  make(map[Key]struct{}),
		fallback: make(map[string]struct{}),
	}
}

// Add records key, reporting whether it was not already present.
//
// The report is what makes the caller's "mark it, and stop if it was already
// marked" a single lookup rather than a Has followed by an Add.
func (s *Set) Add(key string) bool {
	if k, ok := Encode(key); ok {
		if _, found := s.compact[k]; found {
			return false
		}
		s.compact[k] = struct{}{}
		return true
	}
	if _, found := s.fallback[key]; found {
		return false
	}
	s.fallback[key] = struct{}{}
	return true
}

// Has reports whether key has been added.
func (s *Set) Has(key string) bool {
	if k, ok := Encode(key); ok {
		_, found := s.compact[k]
		return found
	}
	_, found := s.fallback[key]
	return found
}

// Len returns the number of distinct keys held.
func (s *Set) Len() int { return len(s.compact) + len(s.fallback) }
