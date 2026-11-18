package steward

// AddCategoryForTest puts a category on the allowlist until the returned func
// is called.
func AddCategoryForTest(c Category) (remove func()) {
	categories = append(categories, c)
	n := len(categories)
	return func() { categories = categories[:n-1] }
}
