import { Fragment, useRef } from "react";

export type YamlTokenKind =
  | "key"
  | "string"
  | "number"
  | "literal"
  | "comment"
  | "punct"
  | "anchor"
  | "plain";
export type YamlToken = { kind: YamlTokenKind; text: string };

const keyPattern =
  /^(\s*(?:-\s+)*)("[^"]*"|'[^']*'|[^\s#'"{[][^:#]*?)(\s*:)(?=\s|$)/;
const scalarPattern =
  /^("(?:[^"\\]|\\.)*"?|'(?:[^']|'')*'?|[&*][^\s,\]}]+|[|>][-+]?\d*|(?:\$\{[^}\s]*\}|[^\s#,[\]{}])+(?:[ \t]+(?:\$\{[^}\s]*\}|[^\s#,[\]{}])+)*)/;

function scalarKind(text: string): YamlTokenKind {
  if (/^["']/.test(text) || /^[|>]/.test(text)) return "string";
  if (/^[&*]/.test(text)) return "anchor";
  if (/^[-+]?(\d[\d_]*(\.\d+)?([eE][-+]?\d+)?|0x[\da-fA-F]+|\.inf|\.nan)$/.test(text))
    return "number";
  if (/^(true|false|null|~|yes|no|on|off)$/i.test(text)) return "literal";
  return "plain";
}

// Line-oriented highlighter: good enough for scenario files, and it never
// drops characters, so the overlay stays aligned with the textarea.
export function tokenizeYamlLine(line: string): YamlToken[] {
  const tokens: YamlToken[] = [];
  const push = (kind: YamlTokenKind, text: string) => {
    if (text) tokens.push({ kind, text });
  };
  let rest = line;
  if (/^(---|\.\.\.)(\s|$)/.test(rest)) {
    push("punct", rest.slice(0, 3));
    rest = rest.slice(3);
  }
  const key = keyPattern.exec(rest);
  if (key) {
    const [whole, lead, name, colon] = key;
    const indent = /^\s*/.exec(lead)![0];
    push("plain", indent);
    push("punct", lead.slice(indent.length));
    push("key", name);
    push("punct", colon);
    rest = rest.slice(whole.length);
  }
  while (rest) {
    const space = /^[ \t]+/.exec(rest);
    if (space) {
      push("plain", space[0]);
      rest = rest.slice(space[0].length);
      continue;
    }
    if (rest[0] === "#") {
      push("comment", rest);
      break;
    }
    if (/^(-(?=\s|$)|[,[\]{}:?])/.test(rest)) {
      push("punct", rest[0]);
      rest = rest.slice(1);
      continue;
    }
    const scalar = scalarPattern.exec(rest);
    const text = scalar ? scalar[0] : rest[0];
    push(scalarKind(text), text);
    rest = rest.slice(text.length);
  }
  return tokens;
}

export function YamlHighlight({ source }: { source: string }) {
  return (
    <>
      {source.split("\n").map((line, i) => (
        <Fragment key={i}>
          {i > 0 && "\n"}
          {tokenizeYamlLine(line).map((t, j) =>
            t.kind === "plain" ? (
              t.text
            ) : (
              <span key={j} className={`yaml-${t.kind}`}>
                {t.text}
              </span>
            ),
          )}
        </Fragment>
      ))}
    </>
  );
}

export function YamlView({ source }: { source: string }) {
  return (
    <pre className="yaml-read">
      <YamlHighlight source={source} />
    </pre>
  );
}

// A transparent textarea over a highlighted copy of its own text: native
// editing, selection and undo stay intact while the colours come from the pre.
export function YamlEditor({
  value,
  onChange,
  label,
}: {
  value: string;
  onChange: (value: string) => void;
  label: string;
}) {
  const mirror = useRef<HTMLPreElement>(null);
  return (
    <div className="yaml-editor">
      <pre ref={mirror} aria-hidden="true">
        <YamlHighlight source={value} />
        {"\n"}
      </pre>
      <textarea
        aria-label={label}
        spellCheck={false}
        wrap="off"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onScroll={(e) => {
          if (!mirror.current) return;
          mirror.current.scrollTop = e.currentTarget.scrollTop;
          mirror.current.scrollLeft = e.currentTarget.scrollLeft;
        }}
      />
    </div>
  );
}
