package layout

type Bounds struct {
	Left, Top, Right, Bottom float64
}

type Item struct {
	Label        string
	ContentLayer string
	Text         string
	BBox         struct {
		Left   float64
		Top    float64
		Right  float64
		Bottom float64
	}
}

type Location struct {
	Ordinal uint32
	Items   []Item
}

type Document struct {
	SchemaVersion string
	Locations     []Location
}
