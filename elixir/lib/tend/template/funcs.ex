defmodule Tend.Template.Funcs do
  @moduledoc """
  The `text/template` built-ins a stored `prompt_md` can call.

  Six of Go's built-ins are here, and they are the six the prompts use:

      and  or  not  eq  ne  len

  `and`, `or` and `not` are the boolean logic in the Dispatch step's ready-set
  prompt; `eq` and `ne` compare a sub-task's state; `len` counts a list. Go's
  other built-ins are not implemented: `lt`, `le`, `gt`, `ge`, `index`,
  `slice`, `printf`, `print`, `println`, `html`, `js`, `urlquery` and `call`.
  No template in the Go tree calls one, and `Tend.Template.Renderer` refuses
  an unknown name with Go's own `function "x" not defined` rather than
  guessing. Adding one is a function clause here plus an entry in
  `@builtins`.

  The four ordering comparisons are the gap worth knowing about. `eq` and
  `ne` are here and `lt`, `le`, `gt` and `ge` are not, so a stored
  `{{if gt .Iteration 1}}` -- an entirely plausible prompt, and one Go
  renders -- is refused with `function "gt" not defined`. They are the first
  four to add if a real `prompt_md` turns out to use one; Go implements them
  in `funcs.go`'s `basicKind` comparison, the same kind check `eq` already
  does here.

  ## and and or return a value, not a boolean

  This is the part an Elixir reflex gets wrong. Go's `and` and `or` are
  variadic, they short-circuit, and they return the *argument that decided*,
  not `true` or `false`:

      {{and 1 2}}        2      -- nothing was false, so the last argument
      {{and 0 2}}        0      -- the first false argument, and 2 is never evaluated
      {{or 0 "x"}}       x      -- the first true argument
      {{or "" 0}}        0      -- nothing was true, so the last argument

  Which is why they are not in `call/2`: the renderer evaluates their
  arguments lazily, one at a time, and stops at the decisive one, so
  `{{and .IsBlocked .Bogus}}` on a false `.IsBlocked` is not an error.
  `short_circuit?/1` marks them.

  ## eq compares within a kind

  Go compares two values only when they are the same *basic kind* -- bool,
  int, float, string -- with one exception, signed against unsigned integers,
  which Elixir has no counterpart for (an Elixir integer is always the signed
  case). So `{{eq 1 1.0}}` is an error in Go, and here, rather than true:

      {{eq .Iteration 3}}        true   -- int64 against an int literal
      {{eq 1 1.0}}               error  -- incompatible types for comparison
      {{eq .State "todo" "doing"}}      -- variadic: == b || == c || ...

  Values that are not a basic kind (a list, a map, a struct, nil) follow Go's
  fallback: nil equals nil and nothing else, two values of different kinds are
  "non-comparable types", and a list or a map is not comparable at all, in Go
  because `==` is not defined on them.

  That last one is the one place where `eq` cannot be Go: Go distinguishes a
  *nil* slice from an empty one, and `{{eq .Outcomes .Outcomes}}` on a nil
  slice is `true` where on an empty one it is `non-comparable type`. Elixir
  has one `[]` for both, and `Tend.Template.Renderer` requires an empty list
  field to hold `[]` rather than `nil`, so this refuses the comparison in
  both cases. Comparing a slice is an error in Go for every non-nil value,
  and no template in the Go tree does it; refusing loudly beats guessing which
  of Go's two answers was meant.

  ## Errors

  Every function returns `{:error, detail}` rather than raising, because Go
  wraps a built-in's error in `error calling eq: ...` and only
  `Tend.Template.Renderer` knows the node to hang the message on. The
  wordings are Go's, with Go's type names replaced by Elixir ones exactly as
  `Tend.Template.RenderError` describes.

  One wording is deliberately *not* Go's, byte for byte.
  `non-comparable type[s]` formats its value slots with `%s`, not `%v`
  (`funcs.go`'s `errBadComparison` neighbours, around line 499), and `%s` on
  a struct whose fields are not all strings emits Go's mismatch garbage:
  `{%!s(int64=43) write the migration done %!s(bool=false) []}`. This uses
  `Tend.Template.Value.format/1` -- `%v` -- in those slots, so the same
  comparison reads `{43 write the migration done false []}`. Reproducing a
  `fmt` verb bug to the byte is not worth a message nobody can act on; every
  other part of the wording, including the value/type/type/value ordering of
  the four-part form, is Go's exactly.
  """

  alias Tend.Template.Value

  # name => {arity, :fixed | :variadic}. For a variadic built-in the arity is
  # the number of *fixed* parameters, which is what Go's
  # `want at least N` counts.
  @builtins %{
    "and" => {1, :variadic},
    "or" => {1, :variadic},
    "not" => {1, :fixed},
    "eq" => {1, :variadic},
    "ne" => {2, :fixed},
    "len" => {1, :fixed}
  }

  @doc """
  Whether `name` is a built-in this port implements.

      iex> Tend.Template.Funcs.defined?("eq")
      true
      iex> Tend.Template.Funcs.defined?("printf")
      false
  """
  @spec defined?(binary()) :: boolean()
  def defined?(name), do: Map.has_key?(@builtins, name)

  @doc """
  Whether `name` is one of the two built-ins that evaluate their arguments
  lazily and return the decisive one -- `and` and `or`.
  """
  @spec short_circuit?(binary()) :: boolean()
  def short_circuit?(name), do: name in ["and", "or"]

  @doc """
  Checks an argument count the way Go's `evalCall` does, before any argument
  is evaluated.

  `final?` says whether a value is arriving from the previous stage of a
  pipeline, which counts as one more argument. Go's `got` for the variadic
  case counts only the written arguments, not the piped one; that is Go's
  quirk and it is reproduced.
  """
  @spec check_arity(binary(), non_neg_integer(), boolean()) :: :ok | {:error, binary()}
  def check_arity(name, count, final?) do
    {arity, kind} = Map.fetch!(@builtins, name)
    given = if final?, do: count + 1, else: count

    case kind do
      :variadic when given < arity ->
        {:error, "wrong number of args for #{name}: want at least #{arity} got #{count}"}

      :fixed when given != arity ->
        {:error, "wrong number of args for #{name}: want #{arity} got #{given}"}

      _ ->
        :ok
    end
  end

  @doc """
  Calls a built-in on already-evaluated arguments.

  `and` and `or` never arrive here -- see `short_circuit?/1`. The argument
  count has already been checked by `check_arity/3`, so a clause that would
  not match one is a renderer bug rather than a template error.
  """
  @spec call(binary(), [term()]) :: {:ok, term()} | {:error, binary()}
  def call("not", [value]), do: {:ok, not Value.truthy?(value)}

  # Go's eq is `a == b || a == c || ...`, so one argument has nothing to
  # compare against.
  def call("eq", [_arg1]), do: {:error, "missing argument for comparison"}
  def call("eq", [arg1 | rest]), do: equal_any(arg1, rest)

  def call("ne", [arg1, arg2]) do
    case equal_any(arg1, [arg2]) do
      {:ok, equal?} -> {:ok, not equal?}
      {:error, detail} -> {:error, detail}
    end
  end

  def call("len", [value]), do: length_of(value)

  ## eq

  defp equal_any(_arg1, []), do: {:ok, false}

  defp equal_any(arg1, [arg | rest]) do
    case equal?(arg1, arg) do
      {:ok, true} -> {:ok, true}
      {:ok, false} -> equal_any(arg1, rest)
      {:error, detail} -> {:error, detail}
    end
  end

  defp equal?(a, b) do
    case {basic_kind(a), basic_kind(b)} do
      {kind, kind} when kind != :invalid ->
        {:ok, a == b}

      {:invalid, :invalid} ->
        compare_other(a, b)

      # nil is not *incompatible* with a string, it simply is not equal to
      # one: Go only raises the mismatch when both sides are valid values,
      # and an untyped nil is not one.
      _mismatch when is_nil(a) or is_nil(b) ->
        {:ok, false}

      # Go's one cross-kind case is a signed integer against an unsigned one,
      # and Elixir has no unsigned kind, so every mismatch that reaches here
      # is one Go also refuses.
      _mismatch ->
        {:error,
         "incompatible types for comparison: " <>
           "#{Value.type_name(a)} and #{Value.type_name(b)}"}
    end
  end

  # Go's fallback for values that are not a basic kind: the same reflect.Kind
  # (or one of them nil) can be compared, nil equals nil, and a type Go's `==`
  # does not accept -- a slice, a map -- is refused by name.
  defp compare_other(a, b) do
    cond do
      not can_compare?(a, b) ->
        {:error,
         "non-comparable types #{Value.format(a)}: #{Value.type_name(a)}, " <>
           "#{Value.type_name(b)}: #{Value.format(b)}"}

      is_nil(a) or is_nil(b) ->
        {:ok, is_nil(a) == is_nil(b)}

      not comparable?(b) ->
        {:error, "non-comparable type #{Value.format(b)}: #{Value.type_name(b)}"}

      true ->
        {:ok, a == b}
    end
  end

  # Go's canCompare: the same kind, or one side nil.
  defp can_compare?(a, b), do: is_nil(a) or is_nil(b) or go_kind(a) == go_kind(b)

  # Go's Type.Comparable(): a slice or a map is not, a struct is when every
  # field is.
  defp comparable?(value) when is_list(value), do: false
  defp comparable?(%_{} = struct), do: struct |> Map.from_struct() |> Enum.all?(&comparable?/1)
  defp comparable?({_key, value}), do: comparable?(value)
  defp comparable?(value) when is_map(value), do: false
  defp comparable?(_value), do: true

  defp basic_kind(value) when is_boolean(value), do: :bool
  defp basic_kind(value) when is_integer(value), do: :int
  defp basic_kind(value) when is_float(value), do: :float
  defp basic_kind(value) when is_binary(value), do: :string
  defp basic_kind(_value), do: :invalid

  defp go_kind(value) when is_list(value), do: :list
  defp go_kind(%_{}), do: :struct
  defp go_kind(value) when is_map(value), do: :map
  defp go_kind(_value), do: :other

  ## len

  # Go's len counts *bytes* in a string, which is what byte_size/1 does.
  defp length_of(value) when is_binary(value), do: {:ok, byte_size(value)}
  defp length_of(value) when is_list(value), do: {:ok, length(value)}
  defp length_of(%_{} = struct), do: {:error, "len of type #{Value.type_name(struct)}"}
  defp length_of(value) when is_map(value), do: {:ok, map_size(value)}

  # Go's `len of nil pointer`, which is what it says for a nil of any type.
  # A literal `{{len nil}}` is the one case where the wording differs and Go's
  # is worse: reflect panics on the zero Value and `text/template` recovers it
  # into `error calling len: reflect: call of reflect.Value.Type on zero
  # Value`. Both refuse the template, which is what matters.
  defp length_of(nil), do: {:error, "len of nil pointer"}
  defp length_of(value), do: {:error, "len of type #{Value.type_name(value)}"}
end
