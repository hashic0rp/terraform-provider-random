// Package provider string.go provides shared functionality between `resource_string` and `resource_password`.
// There is no intent to permanently couple their implementations.
// Over time, they could diverge, or one becomes deprecated.
package provider

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"sort"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// passwordSchemaV1 uses passwordSchemaV0 to obtain the V0 version of the Schema key-value entries but requires that
// the bcrypt_hash entry be configured.
func passwordSchemaV1() map[string]*schema.Schema {
	passwordSchema := passwordSchemaV0()
	passwordSchema["bcrypt_hash"] = &schema.Schema{
		Description: "A bcrypt hash of the generated random string.",
		Type:        schema.TypeString,
		Computed:    true,
		Sensitive:   true,
	}

	return passwordSchema
}

// passwordSchemaV0 uses passwordStringSchema to obtain the default Schema key-value entries but requires that the id
// description, result sensitive and bcrypt_hash entries be configured.
func passwordSchemaV0() map[string]*schema.Schema {
	passwordSchema := passwordStringSchema()
	passwordSchema["id"].Description = "A static value used internally by Terraform, this should not be referenced in configurations."
	passwordSchema["result"].Sensitive = true

	return passwordSchema
}

// stringSchemaV1 uses passwordStringSchema to obtain the default Schema key-value entries but requires that the id
// description be configured.
func stringSchemaV1() map[string]*schema.Schema {
	stringSchema := passwordStringSchema()
	stringSchema["id"].Description = "The generated random string."

	return stringSchema
}

// passwordStringSchema returns map[string]*schema.Schema with all keys and values that are common to both the
// password and string resources.
func passwordStringSchema() map[string]*schema.Schema {
	return map[string]*schema.Schema{
		"keepers": {
			Description: "Arbitrary map of values that, when changed, will trigger recreation of " +
				"resource. See [the main provider documentation](../index.html) for more information.",
			Type:     schema.TypeMap,
			Optional: true,
			ForceNew: true,
		},

		"length": {
			Description: "The length of the string desired. The minimum value for length is 1 and, length " +
				"must also be >= (`min_upper` + `min_lower` + `min_numeric` + `min_special`).",
			Type:             schema.TypeInt,
			Required:         true,
			ForceNew:         true,
			ValidateDiagFunc: validation.ToDiagFunc(validation.IntAtLeast(1)),
		},

		"special": {
			Description: "Include special characters in the result. These are `!@#$%&*()-_=+[]{}<>:?`. Default value is `true`.",
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			ForceNew:    true,
		},

		"upper": {
			Description: "Include uppercase alphabet characters in the result. Default value is `true`.",
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			ForceNew:    true,
		},

		"lower": {
			Description: "Include lowercase alphabet characters in the result. Default value is `true`.",
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			ForceNew:    true,
		},

		"number": {
			Description: "Include numeric characters in the result. Default value is `true`.",
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			ForceNew:    true,
		},

		"min_numeric": {
			Description: "Minimum number of numeric characters in the result. Default value is `0`.",
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     0,
			ForceNew:    true,
		},

		"min_upper": {
			Description: "Minimum number of uppercase alphabet characters in the result. Default value is `0`.",
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     0,
			ForceNew:    true,
		},

		"min_lower": {
			Description: "Minimum number of lowercase alphabet characters in the result. Default value is `0`.",
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     0,
			ForceNew:    true,
		},

		"min_special": {
			Description: "Minimum number of special characters in the result. Default value is `0`.",
			Type:        schema.TypeInt,
			Optional:    true,
			Default:     0,
			ForceNew:    true,
		},

		"override_special": {
			Description: "Supply your own list of special characters to use for string generation.  This " +
				"overrides the default character list in the special argument.  The `special` argument must " +
				"still be set to true for any overwritten characters to be used in generation.",
			Type:     schema.TypeString,
			Optional: true,
			ForceNew: true,
		},

		"result": {
			Description: "The generated random string.",
			Type:        schema.TypeString,
			Computed:    true,
		},

		"id": {
			Computed: true,
			Type:     schema.TypeString,
		},
	}
}

func createStringFunc(sensitive bool) func(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	return func(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
		const numChars = "0123456789"
		const lowerChars = "abcdefghijklmnopqrstuvwxyz"
		const upperChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		var (
			specialChars = "!@#$%&*()-_=+[]{}<>:?"
			diags        diag.Diagnostics
		)

		length := d.Get("length").(int)
		upper := d.Get("upper").(bool)
		minUpper := d.Get("min_upper").(int)
		lower := d.Get("lower").(bool)
		minLower := d.Get("min_lower").(int)
		number := d.Get("number").(bool)
		minNumeric := d.Get("min_numeric").(int)
		special := d.Get("special").(bool)
		minSpecial := d.Get("min_special").(int)
		overrideSpecial := d.Get("override_special").(string)

		if length < minUpper+minLower+minNumeric+minSpecial {
			return append(diags, diag.Diagnostic{
				Severity: diag.Error,
				Summary:  fmt.Sprintf("length (%d) must be >= min_upper + min_lower + min_numeric + min_special (%d)", length, minUpper+minLower+minNumeric+minSpecial),
			})
		}

		if overrideSpecial != "" {
			specialChars = overrideSpecial
		}

		var chars = string("")
		if upper {
			chars += upperChars
		}
		if lower {
			chars += lowerChars
		}
		if number {
			chars += numChars
		}
		if special {
			chars += specialChars
		}

		minMapping := map[string]int{
			numChars:     minNumeric,
			lowerChars:   minLower,
			upperChars:   minUpper,
			specialChars: minSpecial,
		}
		var result = make([]byte, 0, length)
		for k, v := range minMapping {
			s, err := generateRandomBytes(&k, v)
			if err != nil {
				return append(diags, diag.Errorf("error generating random bytes: %s", err)...)
			}
			result = append(result, s...)
		}
		s, err := generateRandomBytes(&chars, length-len(result))
		if err != nil {
			return append(diags, diag.Errorf("error generating random bytes: %s", err)...)
		}
		result = append(result, s...)
		order := make([]byte, len(result))
		if _, err := rand.Read(order); err != nil {
			return append(diags, diag.Errorf("error generating random bytes: %s", err)...)
		}
		sort.Slice(result, func(i, j int) bool {
			return order[i] < order[j]
		})

		if err := d.Set("result", string(result)); err != nil {
			return append(diags, diag.Errorf("error setting result: %s", err)...)
		}

		if sensitive {
			d.SetId("none")
		} else {
			d.SetId(string(result))
		}
		return nil
	}
}

func generateRandomBytes(charSet *string, length int) ([]byte, error) {
	bytes := make([]byte, length)
	setLen := big.NewInt(int64(len(*charSet)))
	for i := range bytes {
		idx, err := rand.Int(rand.Reader, setLen)
		if err != nil {
			return nil, err
		}
		bytes[i] = (*charSet)[idx.Int64()]
	}
	return bytes, nil
}

func readNil(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	return nil
}
