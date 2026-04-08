package firestore

import (
	"math"
	"sort"
	"strings"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// executeQuery runs a StructuredQuery against the store and returns matching documents.
func executeQuery(store *Store, parent string, q *firestorepb.StructuredQuery) []*firestorepb.Document {
	// 1. Determine the collection to query.
	docs := collectFromSources(store, parent, q.GetFrom())

	// 2. Apply WHERE filters.
	if q.GetWhere() != nil {
		docs = applyFilter(docs, q.GetWhere())
	}

	// 3. Apply ORDER BY.
	if len(q.GetOrderBy()) > 0 {
		applyOrderBy(docs, q.GetOrderBy())
	}

	// 4. Apply OFFSET.
	if q.GetOffset() > 0 {
		off := int(q.GetOffset())
		if off >= len(docs) {
			docs = nil
		} else {
			docs = docs[off:]
		}
	}

	// 5. Apply LIMIT.
	if q.GetLimit() != nil {
		lim := int(q.GetLimit().GetValue())
		if lim < len(docs) {
			docs = docs[:lim]
		}
	}

	return docs
}

// collectFromSources gathers all documents from the query's FROM collections.
func collectFromSources(store *Store, parent string, from []*firestorepb.StructuredQuery_CollectionSelector) []*firestorepb.Document {
	if len(from) == 0 {
		return nil
	}
	var all []*firestorepb.Document
	for _, sel := range from {
		colID := sel.GetCollectionId()
		if colID == "" {
			continue
		}
		docs := store.CollectionDocuments(parent, colID)
		all = append(all, docs...)
	}
	return all
}

// applyFilter filters documents based on a StructuredQuery_Filter.
func applyFilter(docs []*firestorepb.Document, f *firestorepb.StructuredQuery_Filter) []*firestorepb.Document {
	switch ft := f.GetFilterType().(type) {
	case *firestorepb.StructuredQuery_Filter_FieldFilter:
		return applyFieldFilter(docs, ft.FieldFilter)
	case *firestorepb.StructuredQuery_Filter_CompositeFilter:
		return applyCompositeFilter(docs, ft.CompositeFilter)
	default:
		return docs
	}
}

func applyFieldFilter(docs []*firestorepb.Document, ff *firestorepb.StructuredQuery_FieldFilter) []*firestorepb.Document {
	fieldPath := ff.GetField().GetFieldPath()
	op := ff.GetOp()
	target := ff.GetValue()

	var result []*firestorepb.Document
	for _, doc := range docs {
		val := getFieldValue(doc.Fields, fieldPath)
		if matchesOp(val, target, op) {
			result = append(result, doc)
		}
	}
	return result
}

func applyCompositeFilter(docs []*firestorepb.Document, cf *firestorepb.StructuredQuery_CompositeFilter) []*firestorepb.Document {
	switch cf.GetOp() {
	case firestorepb.StructuredQuery_CompositeFilter_AND:
		for _, sub := range cf.GetFilters() {
			docs = applyFilter(docs, sub)
		}
		return docs
	case firestorepb.StructuredQuery_CompositeFilter_OR:
		seen := make(map[string]bool)
		var result []*firestorepb.Document
		for _, sub := range cf.GetFilters() {
			matched := applyFilter(docs, sub)
			for _, d := range matched {
				if !seen[d.Name] {
					seen[d.Name] = true
					result = append(result, d)
				}
			}
		}
		return result
	default:
		return docs
	}
}

// getFieldValue retrieves a value from a document's fields by dot-delimited path.
func getFieldValue(fields map[string]*firestorepb.Value, path string) *firestorepb.Value {
	if fields == nil {
		return nil
	}
	parts := strings.Split(path, ".")
	current := fields
	for i, p := range parts {
		v, ok := current[p]
		if !ok {
			return nil
		}
		if i == len(parts)-1 {
			return v
		}
		mv := v.GetMapValue()
		if mv == nil {
			return nil
		}
		current = mv.GetFields()
	}
	return nil
}

// matchesOp evaluates whether docVal <op> target is true.
func matchesOp(docVal, target *firestorepb.Value, op firestorepb.StructuredQuery_FieldFilter_Operator) bool {
	switch op {
	case firestorepb.StructuredQuery_FieldFilter_EQUAL:
		return compareValues(docVal, target) == 0
	case firestorepb.StructuredQuery_FieldFilter_NOT_EQUAL:
		return compareValues(docVal, target) != 0
	case firestorepb.StructuredQuery_FieldFilter_LESS_THAN:
		return compareValues(docVal, target) < 0
	case firestorepb.StructuredQuery_FieldFilter_LESS_THAN_OR_EQUAL:
		return compareValues(docVal, target) <= 0
	case firestorepb.StructuredQuery_FieldFilter_GREATER_THAN:
		return compareValues(docVal, target) > 0
	case firestorepb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL:
		return compareValues(docVal, target) >= 0
	case firestorepb.StructuredQuery_FieldFilter_IN:
		return valueInArray(docVal, target)
	case firestorepb.StructuredQuery_FieldFilter_NOT_IN:
		return !valueInArray(docVal, target)
	case firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS:
		return arrayContains(docVal, target)
	case firestorepb.StructuredQuery_FieldFilter_ARRAY_CONTAINS_ANY:
		return arrayContainsAny(docVal, target)
	default:
		return false
	}
}

// valueInArray returns true if docVal equals any element in target's array.
// Used for IN: where("status", "in", ["active", "pending"]).
func valueInArray(docVal, target *firestorepb.Value) bool {
	arr := target.GetArrayValue()
	if arr == nil {
		return false
	}
	for _, v := range arr.GetValues() {
		if compareValues(docVal, v) == 0 {
			return true
		}
	}
	return false
}

// arrayContains returns true if docVal is an array that contains target.
// Used for ARRAY_CONTAINS: where("tags", "array-contains", "go").
func arrayContains(docVal, target *firestorepb.Value) bool {
	arr := docVal.GetArrayValue()
	if arr == nil {
		return false
	}
	for _, v := range arr.GetValues() {
		if compareValues(v, target) == 0 {
			return true
		}
	}
	return false
}

// arrayContainsAny returns true if docVal is an array that contains any element of target's array.
// Used for ARRAY_CONTAINS_ANY: where("tags", "array-contains-any", ["go", "rust"]).
func arrayContainsAny(docVal, target *firestorepb.Value) bool {
	docArr := docVal.GetArrayValue()
	targetArr := target.GetArrayValue()
	if docArr == nil || targetArr == nil {
		return false
	}
	for _, tv := range targetArr.GetValues() {
		for _, dv := range docArr.GetValues() {
			if compareValues(dv, tv) == 0 {
				return true
			}
		}
	}
	return false
}

// typeOrder returns the Firestore type ordering rank for a Value.
// null=0, boolean=1, number=2, timestamp=3, string=4, bytes=5, reference=6, geo_point=7, array=8, map=9
func typeOrder(v *firestorepb.Value) int {
	if v == nil {
		return -1 // missing fields sort before null
	}
	switch v.GetValueType().(type) {
	case *firestorepb.Value_NullValue:
		return 0
	case *firestorepb.Value_BooleanValue:
		return 1
	case *firestorepb.Value_IntegerValue:
		return 2
	case *firestorepb.Value_DoubleValue:
		return 2 // integers and doubles share the same rank
	case *firestorepb.Value_TimestampValue:
		return 3
	case *firestorepb.Value_StringValue:
		return 4
	case *firestorepb.Value_BytesValue:
		return 5
	case *firestorepb.Value_ReferenceValue:
		return 6
	case *firestorepb.Value_GeoPointValue:
		return 7
	case *firestorepb.Value_ArrayValue:
		return 8
	case *firestorepb.Value_MapValue:
		return 9
	default:
		return 10
	}
}

// compareValues returns -1, 0, or 1 comparing a and b using Firestore ordering.
func compareValues(a, b *firestorepb.Value) int {
	ta := typeOrder(a)
	tb := typeOrder(b)
	if ta != tb {
		return cmpInt(ta, tb)
	}

	// Same type: compare within type.
	if a == nil && b == nil {
		return 0
	}

	switch ta {
	case 0: // null
		return 0
	case 1: // boolean
		ba := boolToInt(a.GetBooleanValue())
		bb := boolToInt(b.GetBooleanValue())
		return cmpInt(ba, bb)
	case 2: // number (integer or double)
		na := toFloat64(a)
		nb := toFloat64(b)
		return cmpFloat(na, nb)
	case 3: // timestamp
		ta := a.GetTimestampValue()
		tb := b.GetTimestampValue()
		if ta.GetSeconds() != tb.GetSeconds() {
			return cmpInt64(ta.GetSeconds(), tb.GetSeconds())
		}
		return cmpInt32(ta.GetNanos(), tb.GetNanos())
	case 4: // string
		return strings.Compare(a.GetStringValue(), b.GetStringValue())
	case 5: // bytes
		ab := a.GetBytesValue()
		bb := b.GetBytesValue()
		return compareBytes(ab, bb)
	case 6: // reference
		return strings.Compare(a.GetReferenceValue(), b.GetReferenceValue())
	default:
		return 0
	}
}

func toFloat64(v *firestorepb.Value) float64 {
	switch v.GetValueType().(type) {
	case *firestorepb.Value_IntegerValue:
		return float64(v.GetIntegerValue())
	case *firestorepb.Value_DoubleValue:
		return v.GetDoubleValue()
	default:
		return 0
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func cmpInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpInt32(a, b int32) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	// Handle NaN: NaN sorts before all other numbers.
	aNaN := math.IsNaN(a)
	bNaN := math.IsNaN(b)
	if aNaN && bNaN {
		return 0
	}
	if aNaN {
		return -1
	}
	if bNaN {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareBytes(a, b []byte) int {
	la := len(a)
	lb := len(b)
	n := la
	if lb < n {
		n = lb
	}
	for i := 0; i < n; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return cmpInt(la, lb)
}

// applyOrderBy sorts documents in place.
func applyOrderBy(docs []*firestorepb.Document, orders []*firestorepb.StructuredQuery_Order) {
	sort.SliceStable(docs, func(i, j int) bool {
		for _, o := range orders {
			fp := o.GetField().GetFieldPath()
			desc := o.GetDirection() == firestorepb.StructuredQuery_DESCENDING

			var vi, vj *firestorepb.Value
			if fp == "__name__" {
				vi = &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: docs[i].Name}}
				vj = &firestorepb.Value{ValueType: &firestorepb.Value_StringValue{StringValue: docs[j].Name}}
			} else {
				vi = getFieldValue(docs[i].Fields, fp)
				vj = getFieldValue(docs[j].Fields, fp)
			}

			cmp := compareValues(vi, vj)
			if cmp == 0 {
				continue
			}
			if desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
}

// buildLimitValue is a helper to create a wrapped Int32Value for queries.
func buildLimitValue(n int32) *wrapperspb.Int32Value {
	return &wrapperspb.Int32Value{Value: n}
}
