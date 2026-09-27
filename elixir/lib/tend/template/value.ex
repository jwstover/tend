defmodule Tend.Template.Value do
  @moduledoc """
  Go's value semantics for the two questions a template keeps asking: is this
  true, and how does it print?

  `Tend.Template.Renderer` asks the first for `{{if}}` and `{{range}}` and the
  second for every `{{action}}` it writes; `Tend.Template.Funcs` asks the first
  for `and`, `or` and `not` and the second when a built-in has to name a value
  in an error message. Both answers are Go's, not Elixir's, so they live in one
  place rather than being reimplemented on either side.
  """

  ## Go's truthiness -- text/template's isTrue

  @doc """
  Go's `isTrue`: what `{{if}}`, `{{range}}`'s `{{else}}` arm and `and` / `or`
  / `not` all mean by "true".

  An empty string, an empty list and an empty map are false, as are zero and
  nil; a struct is always true, however empty it is. The one that bites is the
  number: `{{if $i}}` on index 0 takes the `{{else}}` arm.

      iex> Tend.Template.Value.truthy?(0)
      false
      iex> Tend.Template.Value.truthy?([])
      false
  """
  @spec truthy?(term()) :: boolean()
  def truthy?(nil), do: false
  def truthy?(false), do: false
  def truthy?(true), do: true
  def truthy?(""), do: false
  def truthy?([]), do: false
  def truthy?(value) when is_number(value), do: value != 0
  def truthy?(%_{}), do: true
  def truthy?(value) when is_map(value), do: map_size(value) > 0
  def truthy?(_value), do: true

  ## Go's %v -- what fmt.Fprint writes

  @doc """
  Go's `%v`, which is what `text/template` writes for an action's value.

  Every type a prompt can reach through its data prints Go's bytes. A float
  *literal* is the one exception -- see the comment on `format_float/1`, which
  names the three cases and says why porting `strconv` is not worth it.

      iex> Tend.Template.Value.format([1, 2])
      "[1 2]"
      iex> Tend.Template.Value.format(nil)
      "<no value>"
  """
  @spec format(term()) :: binary()
  # `fmt.Fprint` on a nil interface, which is what an absent Elixir value is
  # closest to.
  def format(nil), do: "<no value>"
  def format(true), do: "true"
  def format(false), do: "false"
  def format(value) when is_binary(value), do: value
  def format(value) when is_integer(value), do: Integer.to_string(value)
  def format(value) when is_float(value), do: format_float(value)
  def format(value) when is_list(value), do: "[" <> Enum.map_join(value, " ", &format/1) <> "]"

  # Go prints a struct's fields in declaration order, and so does this.
  # `defstruct` records the order and `Module.__info__(:struct)` hands it back
  # in it, so a bare `{{.Subtasks}}` is Go's bytes exactly as long as the
  # Elixir struct declares its fields in the Go struct's order -- which the
  # prompt-data structs have to anyway, since they are the same struct.
  # `Map.from_struct/1` would give atom table order, which is neither Go's nor
  # the same twice.
  def format(%module{} = struct) do
    "{" <> Enum.map_join(field_values(struct, module), " ", &format/1) <> "}"
  end

  def format(value) when is_map(value) do
    body =
      value
      |> Enum.sort()
      |> Enum.map_join(" ", fn {key, entry} -> "#{format(key)}:#{format(entry)}" end)

    "map[" <> body <> "]"
  end

  def format(value) when is_atom(value), do: Atom.to_string(value)
  def format(value), do: inspect(value)

  # A struct whose module is not loaded -- a hand-built `%{__struct__: Mod}`,
  # which no prompt data is -- has no declaration order to recover, so its
  # fields are sorted by name: still not Go's order, but the same every run.
  defp field_values(struct, module) do
    case declared_fields(module) do
      fields when is_list(fields) -> Enum.map(fields, &Map.get(struct, &1.field))
      _other -> struct |> Map.from_struct() |> Enum.sort() |> Enum.map(&elem(&1, 1))
    end
  end

  defp declared_fields(module) do
    if Code.ensure_loaded?(module), do: module.__info__(:struct)
  rescue
    UndefinedFunctionError -> nil
  end

  # Go's %v for a float is `strconv.FormatFloat(v, 'g', -1, 64)`: the
  # shortest digits that round-trip, printed in decimal form or in exponent
  # form. `1.0` prints as `1` and `1.5` as `1.5`, both of which this matches.
  #
  # **Where this stops being Go, and it is sooner than it looks.** `g` picks
  # the exponent form when the decimal exponent is `< -4` or `>= eprec`, and
  # for the shortest precision `ftoa.go` pins `eprec` at 6 whatever the digit
  # count is. So Go goes exponential at a *million*, not at some absurd
  # magnitude, and this port does not:
  #
  #     {{1e6}}              Go 1e+06             here 1000000
  #     {{1000000.0}}        Go 1e+06             here 1000000
  #     {{2.5e10}}           Go 2.5e+10           here 25000000000
  #     {{1234567890123.0}}  Go 1.234567890123e+12  here 1234567890123
  #     {{0.00001}}          Go 1e-05             here 1.0e-5
  #     {{1e20}}             Go 1e+20             here 100000000000000000000
  #
  # The values either side of the threshold do match -- `{{999999.0}}` is
  # `999999` in both and `{{0.0001}}` is `0.0001` in both -- so the gap is
  # the threshold and the exponent's spelling (Go zero-pads the exponent to
  # two digits and never writes the mantissa's `.0`), not the digits.
  # Negative zero is a third divergence: Go prints `-0` and this prints `0`,
  # because `trunc(-0.0)` is `0`.
  #
  # None of it is reachable from data: `PromptData` has no float field, so
  # only a float *literal* written into a prompt reaches this clause at all.
  # Porting strconv's `ftoa` is left to whoever first needs one.
  # `Tend.Template.ValueTest` pins every line of the table above.
  defp format_float(value) do
    truncated = trunc(value)

    if value == truncated do
      Integer.to_string(truncated)
    else
      :erlang.float_to_binary(value, [:short])
    end
  end

  ## Naming a type in an error message

  @doc """
  Names a value's type for an error message.

  Go names the Go type (`main.PromptData`, `string`, `[]main.PromptSubtask`)
  and there is no such type here, so a struct is named by its module and
  everything else by its Elixir type. `Tend.Template.RenderError` documents
  the substitution.

      iex> Tend.Template.Value.type_name("s")
      "binary"
  """
  @spec type_name(term()) :: binary()
  def type_name(%module{}), do: inspect(module)
  def type_name(value) when is_binary(value), do: "binary"
  def type_name(value) when is_integer(value), do: "integer"
  def type_name(value) when is_float(value), do: "float"
  def type_name(value) when is_boolean(value), do: "boolean"
  def type_name(value) when is_list(value), do: "list"
  def type_name(nil), do: "nil"
  def type_name(value) when is_map(value), do: "map"
  def type_name(value) when is_atom(value), do: "atom"
  def type_name(value) when is_tuple(value), do: "tuple"
  def type_name(value) when is_function(value), do: "function"
  def type_name(_value), do: "term"
end
