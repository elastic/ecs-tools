// Licensed to Elasticsearch B.V. under one or more agreements.
// Elasticsearch B.V. licenses this file to you under the Apache 2.0 License.
// See the LICENSE file in the project root for more information.

package field

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoErrorf(t, err, "read testdata/%s", name)
	return data
}

func TestParse(t *testing.T) {
	data := mustReadTestdata(t, "ecs_nested.yml")

	schema, err := Parse(data)
	require.NoError(t, err)

	t.Run("fieldsets", func(t *testing.T) {
		wantFieldsets := []string{"base", "event", "os", "process"}
		require.Len(t, schema.Fieldsets, len(wantFieldsets))

		// Fieldsets are sorted by name.
		for i, fs := range schema.Fieldsets {
			assert.Equal(t, wantFieldsets[i], fs.Name)
		}
	})

	t.Run("fieldset top_level", func(t *testing.T) {
		for _, fs := range schema.Fieldsets {
			switch fs.Name {
			case "os":
				assert.False(t, fs.TopLevel, "os fieldset: reusable.top_level=false")
			case "base", "event", "process":
				assert.True(t, fs.TopLevel, "%s fieldset: expected TopLevel=true", fs.Name)
			}
		}
	})

	t.Run("fields from top-level fieldsets only", func(t *testing.T) {
		// Fields from the os fieldset (top_level=false) must not appear.
		for _, f := range schema.Fields {
			assert.NotEqual(t, "os.name", f.Name)
			assert.NotEqual(t, "os.type", f.Name)
		}
	})

	t.Run("fields sorted", func(t *testing.T) {
		for i := 1; i < len(schema.Fields); i++ {
			assert.LessOrEqual(t, schema.Fields[i-1].Name, schema.Fields[i].Name)
		}
	})

	t.Run("field attributes", func(t *testing.T) {
		findField := func(name string) *Field {
			for _, f := range schema.Fields {
				if f.Name == name {
					return f
				}
			}
			return nil
		}

		ts := findField("@timestamp")
		require.NotNil(t, ts, "field @timestamp not found")
		assert.Equal(t, "date", ts.Type)
		assert.Equal(t, "core", ts.Level)

		args := findField("process.args")
		require.NotNil(t, args, "field process.args not found")
		assert.True(t, args.IsArray, "process.args: expected IsArray=true (normalize: [array])")
	})

	t.Run("allowed values", func(t *testing.T) {
		var category *Field
		for _, f := range schema.Fields {
			if f.Name == "event.category" {
				category = f
				break
			}
		}
		require.NotNil(t, category, "field event.category not found")
		require.Len(t, category.AllowedValues, 2)

		names := []string{category.AllowedValues[0].Name, category.AllowedValues[1].Name}
		assert.Contains(t, names, "authentication")
		assert.Contains(t, names, "network")
	})

	t.Run("expected event types", func(t *testing.T) {
		require.NotEmpty(t, schema.ExpectedEventTypes)

		// Sorted by category name.
		for i := 1; i < len(schema.ExpectedEventTypes); i++ {
			assert.LessOrEqual(t, schema.ExpectedEventTypes[i-1].Category, schema.ExpectedEventTypes[i].Category)
		}

		findEET := func(category string) *ExpectedEventType {
			for _, e := range schema.ExpectedEventTypes {
				if e.Category == category {
					return e
				}
			}
			return nil
		}

		auth := findEET("authentication")
		require.NotNil(t, auth, "ExpectedEventType for 'authentication' not found")
		assert.Contains(t, auth.Types, "start")
		assert.Contains(t, auth.Types, "end")
		assert.Contains(t, auth.Types, "info")
	})
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte(":\tinvalid: yaml: ["))
	assert.Error(t, err)
}

func TestParseEmpty(t *testing.T) {
	schema, err := Parse([]byte("{}"))
	require.NoError(t, err)
	assert.Empty(t, schema.Fieldsets)
	assert.Empty(t, schema.Fields)
}
