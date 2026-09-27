defmodule Tend.Template.Lexer do
  @moduledoc """
  Turns Go `text/template` source into a flat token list.

  A near-transliteration of `text/template/parse/lex.go`, because what matters
  is the behaviour Go actually has rather than the one its documentation
  implies. The trim markers in particular were pinned against a real Go build
  before this was written:

    * `{{-` trims only when the `-` sits immediately after `{{` *and* is
      followed by one of space, tab, carriage return or newline. `{{-3}}` is
      therefore the number `-3`, while `{{- 3}}` is a trimmed `3`.
    * `-}}` trims only when the `-` is immediately preceded by one of those
      same four characters and immediately followed by `}}`. `{{.X-}}` and
      `{{.X - }}` are both errors, not trims.
    * Trimming removes the *whole* run of those four characters, and only
      those four: a vertical tab or a NUL survives.

  Tokens are `{type, value, offset}`, with `offset` the 0-based byte offset of
  the token's first byte and `value` `nil` for types that carry none. Spaces
  inside an action are tokens of their own, as they are in Go, because they
  are what separates `{{.A .B}}` (two arguments) from `{{.A.B}}` (one field
  chain).

  The lexer raises `Tend.Template.ParseError`; `Tend.Template.parse/1` turns
  that back into an `{:error, _}` tuple.
  """

  alias Tend.Template.ParseError

  @typedoc """
  One token. `offset` is the byte offset of the token's first byte.

  `:string` carries `{unquoted, as_written}`; `:number` and `:char_constant`
  carry the literal as written, which the parser converts; `:keyword` carries
  one of the control-word atoms.
  """
  @type token :: {atom(), term(), non_neg_integer()}

  # Go's spaceChars. Nothing else is whitespace for trimming purposes.
  @space_chars ~c" \t\r\n"

  @keywords %{
    "block" => :block,
    "break" => :break,
    "continue" => :continue,
    "define" => :define,
    "else" => :else,
    "end" => :end,
    "if" => :if,
    "range" => :range,
    "template" => :template,
    "with" => :with
  }

  @decimal ~c"0123456789_"
  @hex ~c"0123456789abcdefABCDEF_"
  @hex_digits ~c"0123456789abcdefABCDEF"
  @octal ~c"01234567_"
  @binary_digits ~c"01_"

  @simple_escapes %{
    ?a => 7,
    ?b => 8,
    ?f => 12,
    ?n => ?\n,
    ?r => ?\r,
    ?t => ?\t,
    ?v => 11,
    ?\\ => ?\\,
    ?' => ?',
    ?" => ?"
  }

  @doc """
  Tokenizes `source`, always ending in an `:eof` token.

  Raises `Tend.Template.ParseError` on a lexical failure: an unclosed action,
  an unterminated string, a stray character inside an action, an escape that
  names no encodable character.
  """
  @spec tokenize(binary()) :: [token()]
  def tokenize(source) when is_binary(source) do
    source |> lex_text(0, 0, []) |> Enum.reverse()
  end

  @doc """
  Renders a token the way Go's `item.String` does, for error messages:
  keywords and the cursor in angle brackets, `EOF` bare, everything else
  quoted and clipped to ten characters.
  """
  @spec describe(token()) :: String.t()
  def describe({:eof, _, _}), do: "EOF"
  def describe({:keyword, word, _}), do: "<#{word}>"
  def describe({:dot, _, _}), do: "<.>"
  def describe({:nil_literal, _, _}), do: "<nil>"
  def describe(token), do: inspect(clip(lexeme(token)))

  @doc """
  The token's source text, as written.
  """
  @spec lexeme(token()) :: String.t()
  def lexeme({:eof, _, _}), do: ""
  def lexeme({:text, text, _}), do: text
  def lexeme({:left_delim, _, _}), do: "{{"
  def lexeme({:right_delim, _, _}), do: "}}"
  def lexeme({:space, _, _}), do: " "
  def lexeme({:pipe, _, _}), do: "|"
  def lexeme({:left_paren, _, _}), do: "("
  def lexeme({:right_paren, _, _}), do: ")"
  def lexeme({:declare, _, _}), do: ":="
  def lexeme({:assign, _, _}), do: "="
  def lexeme({:dot, _, _}), do: "."
  def lexeme({:nil_literal, _, _}), do: "nil"
  def lexeme({:char, char, _}), do: char
  def lexeme({:field, name, _}), do: "." <> name
  def lexeme({:variable, name, _}), do: name
  def lexeme({:identifier, name, _}), do: name
  def lexeme({:keyword, word, _}), do: to_string(word)
  def lexeme({:bool, value, _}), do: to_string(value)
  def lexeme({:number, text, _}), do: text
  def lexeme({:char_constant, text, _}), do: text
  def lexeme({:string, {_unquoted, text}, _}), do: text

  @doc """
  Expands the escapes in the body of an interpreted string or character
  literal. Exposed so the parser can reuse it for character constants.
  """
  @spec unescape(binary(), non_neg_integer(), binary()) :: binary()
  def unescape(source, offset, body), do: do_unescape(source, offset, body, [])

  defp clip(text) when byte_size(text) > 10, do: binary_part(text, 0, 10) <> "..."
  defp clip(text), do: text

  ## Literal text, between actions

  defp lex_text(src, start, pos, acc) do
    case next_left_delim(src, pos) do
      :nomatch ->
        [{:eof, nil, byte_size(src)} | emit_text(src, start, byte_size(src), acc)]

      delim ->
        {body, trim?} = after_left_delim(src, delim)
        text_end = if trim?, do: trim_trailing(src, start, delim), else: delim
        acc = [{:left_delim, nil, delim} | emit_text(src, start, text_end, acc)]
        lex_action(src, body, 0, acc)
    end
  end

  defp next_left_delim(src, pos) when pos >= byte_size(src), do: :nomatch

  defp next_left_delim(src, pos) do
    case :binary.match(src, "{{", scope: {pos, byte_size(src) - pos}) do
      :nomatch -> :nomatch
      {at, 2} -> at
    end
  end

  defp emit_text(_src, from, to, acc) when to <= from, do: acc

  defp emit_text(src, from, to, acc),
    do: [{:text, binary_part(src, from, to - from), from} | acc]

  # `{{-` plus exactly one space character is the marker; the space is part
  # of it, and any further spaces are ordinary action whitespace.
  defp after_left_delim(src, delim) do
    if byte_at(src, delim + 2) == ?- and space?(byte_at(src, delim + 3)) do
      {delim + 4, true}
    else
      {delim + 2, false}
    end
  end

  defp trim_trailing(src, start, pos) when pos > start do
    if space?(byte_at(src, pos - 1)), do: trim_trailing(src, start, pos - 1), else: pos
  end

  defp trim_trailing(_src, start, _pos), do: start

  defp trim_leading(src, pos) do
    if space?(byte_at(src, pos)), do: trim_leading(src, pos + 1), else: pos
  end

  ## Inside an action

  defp lex_action(src, pos, depth, acc) do
    cond do
      at?(src, pos, "}}") ->
        close_action(src, pos, pos + 2, pos + 2, depth, acc)

      space?(byte_at(src, pos)) ->
        lex_space(src, pos, depth, acc)

      pos >= byte_size(src) ->
        raise ParseError.new(src, pos, "", "unclosed action")

      true ->
        {token, next, depth} = lex_item(src, pos, depth)
        lex_action(src, next, depth, [token | acc])
    end
  end

  # A run of spaces, which may turn out to be the space half of a ` -}}`.
  defp lex_space(src, pos, depth, acc) do
    stop = trim_leading(src, pos)
    acc = [{:space, nil, pos} | acc]

    if at?(src, stop, "-}}") do
      close_action(src, stop, stop + 3, trim_leading(src, stop + 3), depth, acc)
    else
      lex_action(src, stop, depth, acc)
    end
  end

  # `delim` is where the closing delimiter starts, `stop` where it ends and
  # `text` where the following literal run begins, past any trimmed space.
  defp close_action(src, delim, _stop, _text, depth, _acc) when depth > 0 do
    raise ParseError.new(src, delim, "}}", "unclosed left paren")
  end

  defp close_action(src, delim, stop, text, _depth, acc) do
    lex_text(src, text, stop, [{:right_delim, nil, delim} | acc])
  end

  defp lex_item(src, pos, depth) do
    char = byte_at(src, pos)

    cond do
      char == ?= -> {{:assign, nil, pos}, pos + 1, depth}
      char == ?: -> lex_declare(src, pos, depth)
      char == ?| -> {{:pipe, nil, pos}, pos + 1, depth}
      char == ?" -> with_depth(lex_quoted(src, pos), depth)
      char == ?` -> with_depth(lex_raw_quoted(src, pos), depth)
      char == ?$ -> with_depth(lex_field_or_variable(src, pos, :variable), depth)
      char == ?' -> with_depth(lex_char_constant(src, pos), depth)
      char == ?. -> with_depth(lex_dot(src, pos), depth)
      char in ~c"+-" or char in ?0..?9 -> with_depth(lex_number(src, pos), depth)
      alphanumeric?(char) -> with_depth(lex_identifier(src, pos), depth)
      char == ?( -> {{:left_paren, nil, pos}, pos + 1, depth + 1}
      char == ?) -> lex_right_paren(src, pos, depth)
      printable_ascii?(char) -> {{:char, <<char>>, pos}, pos + 1, depth}
      true -> raise unrecognized(src, pos)
    end
  end

  defp with_depth({token, next}, depth), do: {token, next, depth}

  defp lex_declare(src, pos, depth) do
    if at?(src, pos, ":=") do
      {{:declare, nil, pos}, pos + 2, depth}
    else
      raise ParseError.new(src, pos, ":", "expected :=")
    end
  end

  defp lex_right_paren(src, pos, 0) do
    raise ParseError.new(src, pos, ")", "unexpected right paren")
  end

  defp lex_right_paren(_src, pos, depth), do: {{:right_paren, nil, pos}, pos + 1, depth - 1}

  # A `.` starts a number when a digit follows it and a field otherwise.
  defp lex_dot(src, pos) do
    next = byte_at(src, pos + 1)

    if next != nil and next in ?0..?9 do
      lex_number(src, pos)
    else
      lex_field_or_variable(src, pos, :field)
    end
  end

  defp lex_field_or_variable(src, pos, kind) do
    start = pos + 1

    cond do
      terminator?(src, start) and kind == :variable -> {{:variable, "$", pos}, start}
      terminator?(src, start) -> {{:dot, nil, pos}, start}
      true -> named_field_or_variable(src, pos, start, kind)
    end
  end

  defp named_field_or_variable(src, pos, start, kind) do
    stop = scan_alphanumeric(src, start)
    if not terminator?(src, stop), do: raise(bad_character(src, stop))
    name = binary_part(src, start, stop - start)

    case kind do
      :variable -> {{:variable, "$" <> name, pos}, stop}
      :field -> {{:field, name, pos}, stop}
    end
  end

  defp lex_identifier(src, pos) do
    stop = scan_alphanumeric(src, pos)
    if not terminator?(src, stop), do: raise(bad_character(src, stop))

    token =
      case binary_part(src, pos, stop - pos) do
        "nil" -> {:nil_literal, nil, pos}
        "true" -> {:bool, true, pos}
        "false" -> {:bool, false, pos}
        word -> keyword_or_identifier(word, pos)
      end

    {token, stop}
  end

  defp keyword_or_identifier(word, pos) do
    case Map.fetch(@keywords, word) do
      {:ok, keyword} -> {:keyword, keyword, pos}
      :error -> {:identifier, word, pos}
    end
  end

  ## Literals

  defp lex_quoted(src, pos) do
    stop = scan_quoted(src, pos + 1, ?", "unterminated quoted string")
    text = binary_part(src, pos, stop - pos)
    body = binary_part(text, 1, byte_size(text) - 2)
    {{:string, {unescape(src, pos, body), text}, pos}, stop}
  end

  defp lex_raw_quoted(src, pos) do
    stop = scan_raw_quoted(src, pos)
    text = binary_part(src, pos, stop - pos)
    body = binary_part(text, 1, byte_size(text) - 2)
    # Go's spec drops carriage returns from raw string literals.
    {{:string, {String.replace(body, "\r", ""), text}, pos}, stop}
  end

  defp scan_raw_quoted(src, pos) do
    start = pos + 1

    if start >= byte_size(src) do
      raise ParseError.new(src, pos, "`", "unterminated raw quoted string")
    end

    case :binary.match(src, "`", scope: {start, byte_size(src) - start}) do
      :nomatch -> raise ParseError.new(src, pos, "`", "unterminated raw quoted string")
      {at, 1} -> at + 1
    end
  end

  defp lex_char_constant(src, pos) do
    stop = scan_quoted(src, pos + 1, ?', "unterminated character constant")
    {{:char_constant, binary_part(src, pos, stop - pos), pos}, stop}
  end

  # Scans to the closing `quote`, honouring backslash escapes. An embedded
  # newline or EOF is an error, exactly as in Go.
  defp scan_quoted(src, pos, quote, detail) do
    case byte_at(src, pos) do
      nil -> raise ParseError.new(src, pos, "", detail)
      ?\n -> raise ParseError.new(src, pos, "\n", detail)
      ^quote -> pos + 1
      ?\\ -> scan_escape(src, pos, quote, detail)
      _ -> scan_quoted(src, pos + 1, quote, detail)
    end
  end

  defp scan_escape(src, pos, quote, detail) do
    case byte_at(src, pos + 1) do
      nil -> raise ParseError.new(src, pos, "\\", detail)
      ?\n -> raise ParseError.new(src, pos, "\\", detail)
      _ -> scan_quoted(src, pos + 2, quote, detail)
    end
  end

  defp lex_number(src, pos) do
    case scan_number(src, pos) do
      {:ok, stop} ->
        {{:number, binary_part(src, pos, stop - pos), pos}, stop}

      {:error, stop} ->
        text = binary_part(src, pos, max(stop - pos, 1))
        raise ParseError.new(src, pos, text, ~s(bad number syntax: "#{text}"))
    end
  end

  # A transliteration of Go's scanNumber. The parser does the conversion;
  # this only decides where the literal ends.
  defp scan_number(src, pos) do
    pos = accept(src, pos, ~c"+-")
    {pos, digits} = number_base(src, pos)
    pos = accept_run(src, pos, digits)
    pos = if accept?(src, pos, ~c"."), do: accept_run(src, pos + 1, digits), else: pos
    pos = number_exponent(src, pos, digits, ~c"eE", @decimal)
    pos = number_exponent(src, pos, digits, ~c"pP", @hex)
    pos = accept(src, pos, ~c"i")

    if alphanumeric?(byte_at(src, pos)), do: {:error, pos + 1}, else: {:ok, pos}
  end

  defp number_base(src, pos) do
    if accept?(src, pos, ~c"0") do
      cond do
        accept?(src, pos + 1, ~c"xX") -> {pos + 2, @hex}
        accept?(src, pos + 1, ~c"oO") -> {pos + 2, @octal}
        accept?(src, pos + 1, ~c"bB") -> {pos + 2, @binary_digits}
        true -> {pos + 1, @decimal}
      end
    else
      {pos, @decimal}
    end
  end

  defp number_exponent(src, pos, digits, markers, only_for) do
    if digits == only_for and accept?(src, pos, markers) do
      accept_run(src, accept(src, pos + 1, ~c"+-"), @decimal)
    else
      pos
    end
  end

  defp accept?(src, pos, chars) do
    char = byte_at(src, pos)
    char != nil and char in chars
  end

  defp accept(src, pos, chars), do: if(accept?(src, pos, chars), do: pos + 1, else: pos)

  defp accept_run(src, pos, chars) do
    if accept?(src, pos, chars), do: accept_run(src, pos + 1, chars), else: pos
  end

  ## Escapes

  defp do_unescape(_src, _offset, <<>>, acc), do: IO.iodata_to_binary(Enum.reverse(acc))

  defp do_unescape(src, offset, <<?\\, rest::binary>>, acc) do
    {chunk, rest} = escape(src, offset, rest)
    do_unescape(src, offset, rest, [chunk | acc])
  end

  defp do_unescape(src, offset, <<char, rest::binary>>, acc),
    do: do_unescape(src, offset, rest, [char | acc])

  defp escape(src, offset, <<>>), do: raise(bad_escape(src, offset, "\\"))

  defp escape(src, offset, <<marker, rest::binary>>) when marker in ~c"xuU" do
    width = escape_width(marker)

    case rest do
      <<hex::binary-size(width), tail::binary>> ->
        hex_escape(src, offset, marker, hex, tail)

      _ ->
        raise bad_escape(src, offset, <<?\\, marker>> <> rest)
    end
  end

  # An octal escape names one byte, so Go stops at \377: `"\400"` is
  # `invalid syntax` there, and a `ParseError` here rather than an unencodable
  # code point handed to `IO.iodata_to_binary/1`.
  defp escape(src, offset, <<a, b, c, rest::binary>>)
       when a in ?0..?7 and b in ?0..?7 and c in ?0..?7 do
    case Integer.parse(<<a, b, c>>, 8) do
      {value, ""} when value <= 255 -> {value, rest}
      _ -> raise bad_escape(src, offset, <<?\\, a, b, c>>)
    end
  end

  defp escape(src, offset, <<char, rest::binary>>) do
    case Map.fetch(@simple_escapes, char) do
      {:ok, value} -> {value, rest}
      :error -> raise bad_escape(src, offset, <<?\\, char>>)
    end
  end

  defp escape_width(?x), do: 2
  defp escape_width(?u), do: 4
  defp escape_width(?U), do: 8

  # `\x` names one byte; `\u` and `\U` name a code point. Go refuses a
  # surrogate half or anything above U+10FFFF outright -- `"\uD800"` and
  # `"\U00110000"` are both `invalid syntax` -- so neither reaches
  # `<<value::utf8>>`, which would raise an `ArgumentError` past
  # `Tend.Template.parse/1`'s reach.
  defp hex_escape(src, offset, marker, hex, tail) do
    case escape_code(marker, hex) do
      {:ok, chunk} -> {chunk, tail}
      :error -> raise bad_escape(src, offset, <<?\\, marker>> <> hex)
    end
  end

  defp escape_code(marker, hex) do
    if hex_digits?(hex) do
      code_point(marker, String.to_integer(hex, 16))
    else
      :error
    end
  end

  defp code_point(?x, value), do: {:ok, value}

  defp code_point(_marker, value) when value in 0..0xD7FF or value in 0xE000..0x10FFFF,
    do: {:ok, <<value::utf8>>}

  defp code_point(_marker, _value), do: :error

  # `Integer.parse/2` would take a sign, so `"\x-1"` becomes -1 and blows up
  # in `IO.iodata_to_binary/1`. Only hex digits are an escape.
  defp hex_digits?(hex) do
    hex != "" and hex |> :binary.bin_to_list() |> Enum.all?(&(&1 in @hex_digits))
  end

  defp bad_escape(src, offset, text),
    do: ParseError.new(src, offset, text, ~s(invalid escape sequence "#{text}" in string literal))

  ## Character classes

  defp byte_at(src, pos) when pos >= 0 do
    if pos < byte_size(src), do: :binary.at(src, pos)
  end

  defp byte_at(_src, _pos), do: nil

  defp at?(src, pos, prefix) do
    size = byte_size(prefix)
    pos >= 0 and byte_size(src) - pos >= size and binary_part(src, pos, size) == prefix
  end

  defp space?(char), do: char != nil and char in @space_chars

  defp printable_ascii?(nil), do: false
  defp printable_ascii?(char), do: char >= 32 and char < 127

  # Go uses unicode.IsLetter/IsDigit here. Treating every non-ASCII byte as
  # alphanumeric gives the same answer for any identifier a prompt would use
  # and differs only for exotic symbols, which Go rejects and this accepts.
  defp alphanumeric?(nil), do: false

  defp alphanumeric?(char),
    do: char == ?_ or char in ?0..?9 or char in ?a..?z or char in ?A..?Z or char > 127

  defp scan_alphanumeric(src, pos) do
    if alphanumeric?(byte_at(src, pos)), do: scan_alphanumeric(src, pos + 1), else: pos
  end

  # What Go allows immediately after a field, variable or identifier.
  defp terminator?(src, pos) do
    case byte_at(src, pos) do
      nil -> true
      char when char in ~c".,|:()" -> true
      char -> space?(char) or at?(src, pos, "}}")
    end
  end

  defp bad_character(src, pos) do
    {char, text} = codepoint_at(src, pos)
    ParseError.new(src, pos, text, "bad character #{format_rune(char, text)}")
  end

  defp unrecognized(src, pos) do
    {char, text} = codepoint_at(src, pos)
    ParseError.new(src, pos, text, "unrecognized character in action: #{format_rune(char, text)}")
  end

  defp codepoint_at(src, pos) do
    case binary_part(src, pos, byte_size(src) - pos) do
      <<char::utf8, _::binary>> -> {char, <<char::utf8>>}
      <<byte, _::binary>> -> {byte, <<byte>>}
      <<>> -> {0, ""}
    end
  end

  # Go's %#U: U+0041 'A'.
  defp format_rune(char, text) do
    hex = char |> Integer.to_string(16) |> String.upcase() |> String.pad_leading(4, "0")
    "U+#{hex} '#{text}'"
  end
end
