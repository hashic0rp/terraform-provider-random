package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/crypto/bcrypt"

	"github.com/terraform-providers/terraform-provider-random/internal/diagnostics"
	"github.com/terraform-providers/terraform-provider-random/internal/planmodifiers"
	"github.com/terraform-providers/terraform-provider-random/internal/random"
)

var (
	_ resource.Resource                 = (*passwordResource)(nil)
	_ resource.ResourceWithImportState  = (*passwordResource)(nil)
	_ resource.ResourceWithUpgradeState = (*passwordResource)(nil)
)

func NewPasswordResource() resource.Resource {
	return &passwordResource{}
}

type passwordResource struct{}

func (r *passwordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_password"
}

func (r *passwordResource) GetSchema(context.Context) (tfsdk.Schema, diag.Diagnostics) {
	return passwordSchemaV3(), nil
}

func (r *passwordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan passwordModelV3

	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	params := random.StringParams{
		Length:          plan.Length.Value,
		Upper:           plan.Upper.Value,
		MinUpper:        plan.MinUpper.Value,
		Lower:           plan.Lower.Value,
		MinLower:        plan.MinLower.Value,
		Numeric:         plan.Numeric.Value,
		MinNumeric:      plan.MinNumeric.Value,
		Special:         plan.Special.Value,
		MinSpecial:      plan.MinSpecial.Value,
		OverrideSpecial: plan.OverrideSpecial.Value,
	}

	result, err := random.CreateString(params)
	if err != nil {
		resp.Diagnostics.Append(diagnostics.RandomReadError(err.Error())...)
		return
	}

	hash, err := generateHash(string(result))
	if err != nil {
		resp.Diagnostics.Append(diagnostics.HashGenerationError(err.Error())...)
	}

	plan.BcryptHash = types.String{Value: hash}
	plan.ID = types.String{Value: "none"}
	plan.Result = types.String{Value: string(result)}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Read does not need to perform any operations as the state in ReadResourceResponse is already populated.
func (r *passwordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
}

// Update ensures the plan value is copied to the state to complete the update.
func (r *passwordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var model passwordModelV3

	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)

	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

// Delete does not need to explicitly call resp.State.RemoveResource() as this is automatically handled by the
// [framework](https://github.com/hashicorp/terraform-plugin-framework/pull/301).
func (r *passwordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}

func (r *passwordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID

	state := passwordModelV3{
		ID:         types.String{Value: "none"},
		Result:     types.String{Value: id},
		Length:     types.Int64{Value: int64(len(id))},
		Special:    types.Bool{Value: true},
		Upper:      types.Bool{Value: true},
		Lower:      types.Bool{Value: true},
		Number:     types.Bool{Value: true},
		Numeric:    types.Bool{Value: true},
		MinSpecial: types.Int64{Value: 0},
		MinUpper:   types.Int64{Value: 0},
		MinLower:   types.Int64{Value: 0},
		MinNumeric: types.Int64{Value: 0},
		Keepers: types.Map{
			ElemType: types.StringType,
			Null:     true,
		},
		OverrideSpecial: types.String{Null: true},
	}

	hash, err := generateHash(id)
	if err != nil {
		resp.Diagnostics.Append(diagnostics.HashGenerationError(err.Error())...)
	}

	state.BcryptHash = types.String{Value: hash}

	diags := resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *passwordResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	schemaV0 := passwordSchemaV0()
	schemaV1 := passwordSchemaV1()
	schemaV2 := passwordSchemaV2()

	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema:   &schemaV0,
			StateUpgrader: upgradePasswordStateV0toV3,
		},
		1: {
			PriorSchema:   &schemaV1,
			StateUpgrader: upgradePasswordStateV1toV3,
		},
		2: {
			PriorSchema:   &schemaV2,
			StateUpgrader: upgradePasswordStateV2toV3,
		},
	}
}

func upgradePasswordStateV0toV3(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	type modelV0 struct {
		ID              types.String `tfsdk:"id"`
		Keepers         types.Map    `tfsdk:"keepers"`
		Length          types.Int64  `tfsdk:"length"`
		Special         types.Bool   `tfsdk:"special"`
		Upper           types.Bool   `tfsdk:"upper"`
		Lower           types.Bool   `tfsdk:"lower"`
		Number          types.Bool   `tfsdk:"number"`
		MinNumeric      types.Int64  `tfsdk:"min_numeric"`
		MinUpper        types.Int64  `tfsdk:"min_upper"`
		MinLower        types.Int64  `tfsdk:"min_lower"`
		MinSpecial      types.Int64  `tfsdk:"min_special"`
		OverrideSpecial types.String `tfsdk:"override_special"`
		Result          types.String `tfsdk:"result"`
	}

	var passwordDataV0 modelV0

	resp.Diagnostics.Append(req.State.Get(ctx, &passwordDataV0)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Setting fields that can contain null to non-null to prevent forced replacement.
	// This can occur in cases where import has been used in provider versions v3.3.1 and earlier.
	// If import has been used with v3.3.1, for instance then length, lower, number, special, upper,
	// min_lower, min_numeric, min_special and min_upper attributes will all be null in state.
	length := passwordDataV0.Length

	if length.IsNull() {
		length.Null = false
		length.Value = int64(len(passwordDataV0.Result.Value))
	}

	minNumeric := passwordDataV0.MinNumeric

	if minNumeric.IsNull() {
		minNumeric.Null = false
	}

	minUpper := passwordDataV0.MinUpper

	if minUpper.IsNull() {
		minUpper.Null = false
	}

	minLower := passwordDataV0.MinLower

	if minLower.IsNull() {
		minLower.Null = false
	}

	minSpecial := passwordDataV0.MinSpecial

	if minSpecial.IsNull() {
		minSpecial.Null = false
	}

	special := passwordDataV0.Special

	if special.IsNull() {
		special.Null = false
		special.Value = true
	}

	upper := passwordDataV0.Upper

	if upper.IsNull() {
		upper.Null = false
		upper.Value = true
	}

	lower := passwordDataV0.Lower

	if lower.IsNull() {
		lower.Null = false
		lower.Value = true
	}

	number := passwordDataV0.Number

	if number.IsNull() {
		number.Null = false
		number.Value = true
	}

	passwordDataV3 := passwordModelV3{
		Keepers:         passwordDataV0.Keepers,
		Length:          length,
		Special:         special,
		Upper:           upper,
		Lower:           lower,
		Number:          number,
		Numeric:         number,
		MinNumeric:      minNumeric,
		MinUpper:        minUpper,
		MinLower:        minLower,
		MinSpecial:      minSpecial,
		OverrideSpecial: passwordDataV0.OverrideSpecial,
		Result:          passwordDataV0.Result,
		ID:              passwordDataV0.ID,
	}

	hash, err := generateHash(passwordDataV3.Result.Value)
	if err != nil {
		resp.Diagnostics.Append(diagnostics.HashGenerationError(err.Error())...)
		return
	}

	passwordDataV3.BcryptHash.Value = hash

	diags := resp.State.Set(ctx, passwordDataV3)
	resp.Diagnostics.Append(diags...)
}

func upgradePasswordStateV1toV3(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	type modelV1 struct {
		ID              types.String `tfsdk:"id"`
		Keepers         types.Map    `tfsdk:"keepers"`
		Length          types.Int64  `tfsdk:"length"`
		Special         types.Bool   `tfsdk:"special"`
		Upper           types.Bool   `tfsdk:"upper"`
		Lower           types.Bool   `tfsdk:"lower"`
		Number          types.Bool   `tfsdk:"number"`
		MinNumeric      types.Int64  `tfsdk:"min_numeric"`
		MinUpper        types.Int64  `tfsdk:"min_upper"`
		MinLower        types.Int64  `tfsdk:"min_lower"`
		MinSpecial      types.Int64  `tfsdk:"min_special"`
		OverrideSpecial types.String `tfsdk:"override_special"`
		Result          types.String `tfsdk:"result"`
		BcryptHash      types.String `tfsdk:"bcrypt_hash"`
	}

	var passwordDataV1 modelV1

	resp.Diagnostics.Append(req.State.Get(ctx, &passwordDataV1)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Setting fields that can contain null to non-null to prevent forced replacement.
	// This can occur in cases where import has been used in provider versions v3.3.1 and earlier.
	// If import has been used with v3.3.1, for instance then length, lower, number, special, upper,
	// min_lower, min_numeric, min_special and min_upper attributes will all be null in state.
	length := passwordDataV1.Length

	if length.IsNull() {
		length.Null = false
		length.Value = int64(len(passwordDataV1.Result.Value))
	}

	minNumeric := passwordDataV1.MinNumeric

	if minNumeric.IsNull() {
		minNumeric.Null = false
	}

	minUpper := passwordDataV1.MinUpper

	if minUpper.IsNull() {
		minUpper.Null = false
	}

	minLower := passwordDataV1.MinLower

	if minLower.IsNull() {
		minLower.Null = false
	}

	minSpecial := passwordDataV1.MinSpecial

	if minSpecial.IsNull() {
		minSpecial.Null = false
	}

	special := passwordDataV1.Special

	if special.IsNull() {
		special.Null = false
		special.Value = true
	}

	upper := passwordDataV1.Upper

	if upper.IsNull() {
		upper.Null = false
		upper.Value = true
	}

	lower := passwordDataV1.Lower

	if lower.IsNull() {
		lower.Null = false
		lower.Value = true
	}

	number := passwordDataV1.Number

	if number.IsNull() {
		number.Null = false
		number.Value = true
	}

	passwordDataV3 := passwordModelV3{
		Keepers:         passwordDataV1.Keepers,
		Length:          length,
		Special:         special,
		Upper:           upper,
		Lower:           lower,
		Number:          number,
		Numeric:         number,
		MinNumeric:      minNumeric,
		MinUpper:        minUpper,
		MinLower:        minLower,
		MinSpecial:      minSpecial,
		OverrideSpecial: passwordDataV1.OverrideSpecial,
		BcryptHash:      passwordDataV1.BcryptHash,
		Result:          passwordDataV1.Result,
		ID:              passwordDataV1.ID,
	}

	diags := resp.State.Set(ctx, passwordDataV3)
	resp.Diagnostics.Append(diags...)
}

func upgradePasswordStateV2toV3(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	type passwordModelV2 struct {
		ID              types.String `tfsdk:"id"`
		Keepers         types.Map    `tfsdk:"keepers"`
		Length          types.Int64  `tfsdk:"length"`
		Special         types.Bool   `tfsdk:"special"`
		Upper           types.Bool   `tfsdk:"upper"`
		Lower           types.Bool   `tfsdk:"lower"`
		Number          types.Bool   `tfsdk:"number"`
		Numeric         types.Bool   `tfsdk:"numeric"`
		MinNumeric      types.Int64  `tfsdk:"min_numeric"`
		MinUpper        types.Int64  `tfsdk:"min_upper"`
		MinLower        types.Int64  `tfsdk:"min_lower"`
		MinSpecial      types.Int64  `tfsdk:"min_special"`
		OverrideSpecial types.String `tfsdk:"override_special"`
		Result          types.String `tfsdk:"result"`
		BcryptHash      types.String `tfsdk:"bcrypt_hash"`
	}

	var passwordDataV2 passwordModelV2

	resp.Diagnostics.Append(req.State.Get(ctx, &passwordDataV2)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// Setting fields that can contain null to non-null to prevent forced replacement.
	// This can occur in cases where import has been used in provider versions v3.3.1 and earlier.
	// If import has been used with v3.3.1, for instance then length, lower, number, special, upper,
	// min_lower, min_numeric, min_special and min_upper attributes will all be null in state.
	length := passwordDataV2.Length

	if length.IsNull() {
		length.Null = false
		length.Value = int64(len(passwordDataV2.Result.Value))
	}

	minNumeric := passwordDataV2.MinNumeric

	if minNumeric.IsNull() {
		minNumeric.Null = false
	}

	minUpper := passwordDataV2.MinUpper

	if minUpper.IsNull() {
		minUpper.Null = false
	}

	minLower := passwordDataV2.MinLower

	if minLower.IsNull() {
		minLower.Null = false
	}

	minSpecial := passwordDataV2.MinSpecial

	if minSpecial.IsNull() {
		minSpecial.Null = false
	}

	special := passwordDataV2.Special

	if special.IsNull() {
		special.Null = false
		special.Value = true
	}

	upper := passwordDataV2.Upper

	if upper.IsNull() {
		upper.Null = false
		upper.Value = true
	}

	lower := passwordDataV2.Lower

	if lower.IsNull() {
		lower.Null = false
		lower.Value = true
	}

	number := passwordDataV2.Number

	if number.IsNull() {
		number.Null = false
		number.Value = true
	}

	numeric := passwordDataV2.Number

	if numeric.IsNull() {
		numeric.Null = false
		numeric.Value = true
	}

	// Schema version 2 to schema version 3 is a duplicate of the data,
	// however the BcryptHash value may have been incorrectly generated.
	//nolint:gosimple // V3 model will expand over time so all fields are written out to help future code changes.
	passwordDataV3 := passwordModelV3{
		BcryptHash:      passwordDataV2.BcryptHash,
		ID:              passwordDataV2.ID,
		Keepers:         passwordDataV2.Keepers,
		Length:          length,
		Lower:           lower,
		MinLower:        minLower,
		MinNumeric:      minNumeric,
		MinSpecial:      minSpecial,
		MinUpper:        minUpper,
		Number:          number,
		Numeric:         numeric,
		OverrideSpecial: passwordDataV2.OverrideSpecial,
		Result:          passwordDataV2.Result,
		Special:         special,
		Upper:           upper,
	}

	// Set the duplicated data now so we can easily return early below.
	// The BcryptHash value will be adjusted later if it is incorrect.
	resp.Diagnostics.Append(resp.State.Set(ctx, passwordDataV3)...)

	if resp.Diagnostics.HasError() {
		return
	}

	// If the BcryptHash value does not correctly verify against the Result
	// value we should regenerate it.
	err := bcrypt.CompareHashAndPassword([]byte(passwordDataV2.BcryptHash.Value), []byte(passwordDataV2.Result.Value))

	// If the hash matched the password, there is nothing to do.
	if err == nil {
		return
	}

	if !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		resp.Diagnostics.AddError(
			"Version 3 State Upgrade Error",
			"An unexpected error occurred when comparing the state version 2 password and bcrypt hash. "+
				"This is always an issue in the provider and should be reported to the provider developers.\n\n"+
				"Original Error: "+err.Error(),
		)
		return
	}

	// Regenerate the BcryptHash value.
	newBcryptHash, err := bcrypt.GenerateFromPassword([]byte(passwordDataV2.Result.Value), bcrypt.DefaultCost)

	if err != nil {
		resp.Diagnostics.AddError(
			"Version 3 State Upgrade Error",
			"An unexpected error occurred when generating a new password bcrypt hash. "+
				"Check the error below and ensure the system executing Terraform can properly generate randomness.\n\n"+
				"Original Error: "+err.Error(),
		)
		return
	}

	passwordDataV3.BcryptHash.Value = string(newBcryptHash)

	resp.Diagnostics.Append(resp.State.Set(ctx, passwordDataV3)...)
}

func generateHash(toHash string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(toHash), bcrypt.DefaultCost)

	return string(hash), err
}

func passwordSchemaV3() tfsdk.Schema {
	return tfsdk.Schema{
		Version: 3,
		Description: "Identical to [random_string](string.html) with the exception that the result is " +
			"treated as sensitive and, thus, _not_ displayed in console output. Read more about sensitive " +
			"data handling in the " +
			"[Terraform documentation](https://www.terraform.io/docs/language/state/sensitive-data.html).\n\n" +
			"This resource *does* use a cryptographic random number generator.",
		Attributes: map[string]tfsdk.Attribute{
			"keepers": {
				Description: "Arbitrary map of values that, when changed, will trigger recreation of " +
					"resource. See [the main provider documentation](../index.html) for more information.",
				Type: types.MapType{
					ElemType: types.StringType,
				},
				Optional: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.RequiresReplaceIfValuesNotNull(),
				},
			},

			"length": {
				Description: "The length of the string desired. The minimum value for length is 1 and, length " +
					"must also be >= (`min_upper` + `min_lower` + `min_numeric` + `min_special`).",
				Type:     types.Int64Type,
				Required: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.RequiresReplace(),
				},
				Validators: []tfsdk.AttributeValidator{
					int64validator.AtLeast(1),
					int64validator.AtLeastSumOf(
						path.MatchRoot("min_upper"),
						path.MatchRoot("min_lower"),
						path.MatchRoot("min_numeric"),
						path.MatchRoot("min_special"),
					),
				},
			},

			"special": {
				Description: "Include special characters in the result. These are `!@#$%&*()-_=+[]{}<>:?`. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"upper": {
				Description: "Include uppercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"lower": {
				Description: "Include lowercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"number": {
				Description: "Include numeric characters in the result. Default value is `true`. " +
					"**NOTE**: This is deprecated, use `numeric` instead.",
				Type:     types.BoolType,
				Optional: true,
				Computed: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.NumberNumericAttributePlanModifier(),
					planmodifiers.RequiresReplace(),
				},
				DeprecationMessage: "**NOTE**: This is deprecated, use `numeric` instead.",
			},

			"numeric": {
				Description: "Include numeric characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.NumberNumericAttributePlanModifier(),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_numeric": {
				Description: "Minimum number of numeric characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_upper": {
				Description: "Minimum number of uppercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_lower": {
				Description: "Minimum number of lowercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_special": {
				Description: "Minimum number of special characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"override_special": {
				Description: "Supply your own list of special characters to use for string generation.  This " +
					"overrides the default character list in the special argument.  The `special` argument must " +
					"still be set to true for any overwritten characters to be used in generation.",
				Type:     types.StringType,
				Optional: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.RequiresReplaceIf(
						planmodifiers.RequiresReplaceUnlessEmptyStringToNull(),
						"Replace on modification unless updating from empty string (\"\") to null.",
						"Replace on modification unless updating from empty string (`\"\"`) to `null`.",
					),
				},
			},

			"result": {
				Description: "The generated random string.",
				Type:        types.StringType,
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.UseStateForUnknown(),
				},
			},

			"bcrypt_hash": {
				Description: "A bcrypt hash of the generated random string.",
				Type:        types.StringType,
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.UseStateForUnknown(),
				},
			},

			"id": {
				Description: "A static value used internally by Terraform, this should not be referenced in configurations.",
				Computed:    true,
				Type:        types.StringType,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.UseStateForUnknown(),
				},
			},
		},
	}
}

func passwordSchemaV2() tfsdk.Schema {
	return tfsdk.Schema{
		Version: 2,
		Description: "Identical to [random_string](string.html) with the exception that the result is " +
			"treated as sensitive and, thus, _not_ displayed in console output. Read more about sensitive " +
			"data handling in the " +
			"[Terraform documentation](https://www.terraform.io/docs/language/state/sensitive-data.html).\n\n" +
			"This resource *does* use a cryptographic random number generator.",
		Attributes: map[string]tfsdk.Attribute{
			"keepers": {
				Description: "Arbitrary map of values that, when changed, will trigger recreation of " +
					"resource. See [the main provider documentation](../index.html) for more information.",
				Type: types.MapType{
					ElemType: types.StringType,
				},
				Optional: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.RequiresReplaceIfValuesNotNull(),
				},
			},

			"length": {
				Description: "The length of the string desired. The minimum value for length is 1 and, length " +
					"must also be >= (`min_upper` + `min_lower` + `min_numeric` + `min_special`).",
				Type:     types.Int64Type,
				Required: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.RequiresReplace(),
				},
				Validators: []tfsdk.AttributeValidator{
					int64validator.AtLeast(1),
					int64validator.AtLeastSumOf(
						path.MatchRoot("min_upper"),
						path.MatchRoot("min_lower"),
						path.MatchRoot("min_numeric"),
						path.MatchRoot("min_special"),
					),
				},
			},

			"special": {
				Description: "Include special characters in the result. These are `!@#$%&*()-_=+[]{}<>:?`. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"upper": {
				Description: "Include uppercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"lower": {
				Description: "Include lowercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"number": {
				Description: "Include numeric characters in the result. Default value is `true`. " +
					"**NOTE**: This is deprecated, use `numeric` instead.",
				Type:     types.BoolType,
				Optional: true,
				Computed: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.NumberNumericAttributePlanModifier(),
					planmodifiers.RequiresReplace(),
				},
				DeprecationMessage: "**NOTE**: This is deprecated, use `numeric` instead.",
			},

			"numeric": {
				Description: "Include numeric characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.NumberNumericAttributePlanModifier(),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_numeric": {
				Description: "Minimum number of numeric characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_upper": {
				Description: "Minimum number of uppercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_lower": {
				Description: "Minimum number of lowercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_special": {
				Description: "Minimum number of special characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"override_special": {
				Description: "Supply your own list of special characters to use for string generation.  This " +
					"overrides the default character list in the special argument.  The `special` argument must " +
					"still be set to true for any overwritten characters to be used in generation.",
				Type:     types.StringType,
				Optional: true,
				Computed: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.String{Value: ""}),
					resource.RequiresReplace(),
				},
			},

			"result": {
				Description: "The generated random string.",
				Type:        types.StringType,
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.UseStateForUnknown(),
				},
			},

			"bcrypt_hash": {
				Description: "A bcrypt hash of the generated random string.",
				Type:        types.StringType,
				Computed:    true,
				Sensitive:   true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.UseStateForUnknown(),
				},
			},

			"id": {
				Description: "A static value used internally by Terraform, this should not be referenced in configurations.",
				Computed:    true,
				Type:        types.StringType,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.UseStateForUnknown(),
				},
			},
		},
	}
}

func passwordSchemaV1() tfsdk.Schema {
	return tfsdk.Schema{
		Version: 1,
		Description: "Identical to [random_string](string.html) with the exception that the result is " +
			"treated as sensitive and, thus, _not_ displayed in console output. Read more about sensitive " +
			"data handling in the " +
			"[Terraform documentation](https://www.terraform.io/docs/language/state/sensitive-data.html).\n\n" +
			"This resource *does* use a cryptographic random number generator.",
		Attributes: map[string]tfsdk.Attribute{
			"keepers": {
				Description: "Arbitrary map of values that, when changed, will trigger recreation of " +
					"resource. See [the main provider documentation](../index.html) for more information.",
				Type: types.MapType{
					ElemType: types.StringType,
				},
				Optional: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.RequiresReplace(),
				},
			},

			"length": {
				Description: "The length of the string desired. The minimum value for length is 1 and, length " +
					"must also be >= (`min_upper` + `min_lower` + `min_numeric` + `min_special`).",
				Type:          types.Int64Type,
				Required:      true,
				PlanModifiers: []tfsdk.AttributePlanModifier{resource.RequiresReplace()},
				Validators: []tfsdk.AttributeValidator{
					int64validator.AtLeast(1),
					int64validator.AtLeastSumOf(
						path.MatchRoot("min_upper"),
						path.MatchRoot("min_lower"),
						path.MatchRoot("min_numeric"),
						path.MatchRoot("min_special"),
					),
				},
			},

			"special": {
				Description: "Include special characters in the result. These are `!@#$%&*()-_=+[]{}<>:?`. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"upper": {
				Description: "Include uppercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"lower": {
				Description: "Include lowercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"number": {
				Description: "Include numeric characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_numeric": {
				Description: "Minimum number of numeric characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_upper": {
				Description: "Minimum number of uppercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_lower": {
				Description: "Minimum number of lowercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_special": {
				Description: "Minimum number of special characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"override_special": {
				Description: "Supply your own list of special characters to use for string generation.  This " +
					"overrides the default character list in the special argument.  The `special` argument must " +
					"still be set to true for any overwritten characters to be used in generation.",
				Type:     types.StringType,
				Optional: true,
				Computed: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.RequiresReplace(),
				},
			},

			"result": {
				Description: "The generated random string.",
				Type:        types.StringType,
				Computed:    true,
				Sensitive:   true,
			},

			"bcrypt_hash": {
				Description: "A bcrypt hash of the generated random string.",
				Type:        types.StringType,
				Computed:    true,
				Sensitive:   true,
			},

			"id": {
				Description: "A static value used internally by Terraform, this should not be referenced in configurations.",
				Computed:    true,
				Type:        types.StringType,
			},
		},
	}
}

func passwordSchemaV0() tfsdk.Schema {
	return tfsdk.Schema{
		Description: "Identical to [random_string](string.html) with the exception that the result is " +
			"treated as sensitive and, thus, _not_ displayed in console output. Read more about sensitive " +
			"data handling in the " +
			"[Terraform documentation](https://www.terraform.io/docs/language/state/sensitive-data.html).\n\n" +
			"This resource *does* use a cryptographic random number generator.",
		Attributes: map[string]tfsdk.Attribute{
			"keepers": {
				Description: "Arbitrary map of values that, when changed, will trigger recreation of " +
					"resource. See [the main provider documentation](../index.html) for more information.",
				Type: types.MapType{
					ElemType: types.StringType,
				},
				Optional:      true,
				PlanModifiers: []tfsdk.AttributePlanModifier{resource.RequiresReplace()},
			},

			"length": {
				Description: "The length of the string desired. The minimum value for length is 1 and, length " +
					"must also be >= (`min_upper` + `min_lower` + `min_numeric` + `min_special`).",
				Type:          types.Int64Type,
				Required:      true,
				PlanModifiers: []tfsdk.AttributePlanModifier{resource.RequiresReplace()},
				Validators: []tfsdk.AttributeValidator{
					int64validator.AtLeast(1),
					int64validator.AtLeastSumOf(
						path.MatchRoot("min_upper"),
						path.MatchRoot("min_lower"),
						path.MatchRoot("min_numeric"),
						path.MatchRoot("min_special"),
					),
				},
			},

			"special": {
				Description: "Include special characters in the result. These are `!@#$%&*()-_=+[]{}<>:?`. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"upper": {
				Description: "Include uppercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"lower": {
				Description: "Include lowercase alphabet characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"number": {
				Description: "Include numeric characters in the result. Default value is `true`.",
				Type:        types.BoolType,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Bool{Value: true}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_numeric": {
				Description: "Minimum number of numeric characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_upper": {
				Description: "Minimum number of uppercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_lower": {
				Description: "Minimum number of lowercase alphabet characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"min_special": {
				Description: "Minimum number of special characters in the result. Default value is `0`.",
				Type:        types.Int64Type,
				Optional:    true,
				Computed:    true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					planmodifiers.DefaultValue(types.Int64{Value: 0}),
					planmodifiers.RequiresReplace(),
				},
			},

			"override_special": {
				Description: "Supply your own list of special characters to use for string generation.  This " +
					"overrides the default character list in the special argument.  The `special` argument must " +
					"still be set to true for any overwritten characters to be used in generation.",
				Type:     types.StringType,
				Optional: true,
				Computed: true,
				PlanModifiers: []tfsdk.AttributePlanModifier{
					resource.RequiresReplace(),
				},
			},

			"result": {
				Description: "The generated random string.",
				Type:        types.StringType,
				Computed:    true,
				Sensitive:   true,
			},

			"id": {
				Description: "A static value used internally by Terraform, this should not be referenced in configurations.",
				Computed:    true,
				Type:        types.StringType,
			},
		},
	}
}

type passwordModelV3 struct {
	ID              types.String `tfsdk:"id"`
	Keepers         types.Map    `tfsdk:"keepers"`
	Length          types.Int64  `tfsdk:"length"`
	Special         types.Bool   `tfsdk:"special"`
	Upper           types.Bool   `tfsdk:"upper"`
	Lower           types.Bool   `tfsdk:"lower"`
	Number          types.Bool   `tfsdk:"number"`
	Numeric         types.Bool   `tfsdk:"numeric"`
	MinNumeric      types.Int64  `tfsdk:"min_numeric"`
	MinUpper        types.Int64  `tfsdk:"min_upper"`
	MinLower        types.Int64  `tfsdk:"min_lower"`
	MinSpecial      types.Int64  `tfsdk:"min_special"`
	OverrideSpecial types.String `tfsdk:"override_special"`
	Result          types.String `tfsdk:"result"`
	BcryptHash      types.String `tfsdk:"bcrypt_hash"`
}
