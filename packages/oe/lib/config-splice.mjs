// oe runs this script with node to change values in open-e2ee.config.ts:
//
//   node config-splice.mjs < {"source": "...", "changes": [{"path": [...], "value": ...}]}
//
// It parses the source text and never evaluates it. It replaces only the
// literals that change and adds each missing property, so every other byte of
// the file stays. It writes {"source"} with the new text, or {"edit"} when a
// change reaches a value that is not a literal and a person must make it.
import module from "node:module";
import process from "node:process";
import { fail, requireNode } from "./config-node.mjs";
import { Parser } from "./vendor/acorn.mjs";

await requireNode();

let input = "";
process.stdin.setEncoding("utf8");
for await (const chunk of process.stdin) {
  input += chunk;
}
const { source, changes } = JSON.parse(input);

// stripTypeScriptTypes warns that it is experimental. The warning is noise
// for a person who runs oe.
process.removeAllListeners("warning");

let result;
try {
  result = splice(source, changes);
} catch (error) {
  await fail(
    "CONFIG_INVALID",
    `open-e2ee.config.ts does not parse: ${error.message}`,
  );
}
process.stdout.write(`${JSON.stringify(result)}\n`);

function splice(source, changes) {
  const patch = {};
  for (const change of changes) {
    let target = patch;
    for (const key of change.path.slice(0, -1)) {
      if (!isPlainObject(target[key])) {
        target[key] = {};
      }
      target = target[key];
    }
    const key = change.path.at(-1);
    target[key] = merge(target[key], change.value);
  }

  const file = parse(source);
  const newline = source.includes("\r\n") ? "\r\n" : "\n";
  const root = configObject(file.program);
  if (root.type !== "ObjectExpression") {
    return blocked([], patch, root);
  }
  const style = {
    newline,
    quote: firstQuote(root) ?? '"',
    unit: indentUnit(root),
    trailingComma:
      root.properties.length === 0 || commaAfter(root.properties.at(-1)) !== -1,
  };
  const edits = [];
  const refusal = patchObject(root, patch, [], edits, style);
  if (refusal) {
    return refusal;
  }
  let output = source;
  for (const edit of edits.sort((a, b) => b.start - a.start)) {
    output = output.slice(0, edit.start) + edit.text + output.slice(edit.end);
  }
  parse(output);
  return { source: output };

  function parse(text) {
    const comments = [];
    const program = Parser.parse(
      module.stripTypeScriptTypes(text, { mode: "strip" }),
      {
        ecmaVersion: "latest",
        sourceType: "module",
        onComment: comments,
      },
    );
    return { program, comments };
  }

  // configObject returns the object literal that the default export holds, or
  // the expression that holds it when it is not a literal.
  function configObject(program) {
    const exported = program.body.find(
      (node) => node.type === "ExportDefaultDeclaration",
    );
    if (!exported) {
      throw new Error("the file has no default export");
    }
    const defineConfig = new Set();
    const constants = new Map();
    for (const node of program.body) {
      if (
        node.type === "ImportDeclaration" &&
        node.source.value === "@open-e2ee/oe/config"
      ) {
        for (const specifier of node.specifiers) {
          if (
            specifier.type === "ImportSpecifier" &&
            specifier.imported.name === "defineConfig"
          ) {
            defineConfig.add(specifier.local.name);
          }
        }
      }
      if (node.type === "VariableDeclaration" && node.kind === "const") {
        for (const declarator of node.declarations) {
          if (declarator.id.type === "Identifier" && declarator.init) {
            constants.set(declarator.id.name, declarator.init);
          }
        }
      }
    }
    let value = exported.declaration;
    for (const seen = new Set(); ;) {
      if (
        value.type === "CallExpression" &&
        value.callee.type === "Identifier" &&
        defineConfig.has(value.callee.name) &&
        value.arguments.length === 1
      ) {
        value = value.arguments[0];
      } else if (
        value.type === "Identifier" &&
        constants.has(value.name) &&
        !seen.has(value.name)
      ) {
        seen.add(value.name);
        value = constants.get(value.name);
      } else {
        return value;
      }
    }
  }

  // patchObject applies patch to the object literal node. It returns the
  // refusal for the first value that it cannot change.
  function patchObject(node, patch, path, edits, style) {
    const additions = [];
    for (const [key, value] of Object.entries(patch)) {
      const index = node.properties.findLastIndex(
        (property) => propertyName(property) === key,
      );
      const later = node.properties
        .slice(index + 1)
        .find(
          (property) => property.type === "SpreadElement" || property.computed,
        );
      if (later) {
        return blocked([...path, key], value, later);
      }
      if (index === -1) {
        additions.push([key, value]);
        continue;
      }
      const property = node.properties[index];
      if (property.kind !== "init" || property.method || property.shorthand) {
        return blocked(
          [...path, key],
          value,
          property.shorthand ? property.value : property,
        );
      }
      if (isPlainObject(value) && property.value.type === "ObjectExpression") {
        const refusal = patchObject(
          property.value,
          value,
          [...path, key],
          edits,
          style,
        );
        if (refusal) {
          return refusal;
        }
        continue;
      }
      if (!isLiteral(property.value)) {
        return blocked([...path, key], value, property.value);
      }
      if (sameValue(property.value, value)) {
        continue;
      }
      const quote =
        property.value.type === "Literal" &&
        typeof property.value.value === "string"
          ? source[property.value.start]
          : style.quote;
      const indent = lineIndent(property.start);
      edits.push({
        start: property.value.start,
        end: property.value.end,
        text: render(value, indent, multiline(node), { ...style, quote }),
      });
    }
    if (additions.length > 0) {
      edits.push(...addProperties(node, additions, style));
    }
    return undefined;
  }

  function addProperties(node, additions, style) {
    const { newline, unit, trailingComma } = style;
    const lines = (indent, separator) =>
      additions.map(([key, value], index) => {
        const comma = index < additions.length - 1 || separator ? "," : "";
        return `${newline}${indent}${propertyKey(key)}: ${render(value, indent, true, style)}${comma}`;
      });
    if (node.properties.length === 0) {
      const indent = lineIndent(node.start);
      const body = lines(indent + unit, trailingComma).join("");
      return [
        {
          start: node.start,
          end: node.end,
          text: `{${body}${newline}${indent}}`,
        },
      ];
    }
    const last = node.properties.at(-1);
    const comma = commaAfter(last);
    if (!multiline(node)) {
      const text = additions
        .map(
          ([key, value]) =>
            `${propertyKey(key)}: ${render(value, "", false, style)}`,
        )
        .join(", ");
      if (comma === -1) {
        return [{ start: last.end, end: last.end, text: `, ${text}` }];
      }
      return [{ start: comma + 1, end: comma + 1, text: ` ${text},` }];
    }
    const indent = lineIndent(last.start);
    const end = lineEnd(comma === -1 ? last.end : comma + 1);
    const inserted = {
      start: end,
      end,
      text: lines(indent, comma !== -1).join(""),
    };
    if (comma === -1) {
      return [{ start: last.end, end: last.end, text: "," }, inserted];
    }
    return [inserted];
  }

  // commaAfter returns the offset of the comma that follows node, or -1.
  function commaAfter(node) {
    const position = skipBlank(node.end, false);
    return source[position] === "," ? position : -1;
  }

  // lineEnd returns the offset where the line that holds position ends, after
  // any comment on that line, or position when code follows on the line.
  function lineEnd(position) {
    const end = skipBlank(position, true);
    return source[end] === "\r" || source[end] === "\n" || end === source.length
      ? end
      : position;
  }

  // skipBlank skips spaces and comments. With sameLine, it stops at the end
  // of the line.
  function skipBlank(position, sameLine) {
    for (;;) {
      const character = source[position];
      if (
        character === " " ||
        character === "\t" ||
        (!sameLine && (character === "\r" || character === "\n"))
      ) {
        position++;
        continue;
      }
      const comment = file.comments.find(
        (candidate) => candidate.start === position,
      );
      if (
        comment &&
        !(sameLine && source.slice(comment.start, comment.end).includes("\n"))
      ) {
        position = comment.end;
        continue;
      }
      return position;
    }
  }

  function lineIndent(position) {
    const start = source.lastIndexOf("\n", position - 1) + 1;
    return /^[ \t]*/.exec(source.slice(start))[0];
  }

  function multiline(node) {
    return /[\r\n]/.test(source.slice(node.start, node.end));
  }

  function indentUnit(node) {
    if (node.properties.length === 0 || !multiline(node)) {
      return "  ";
    }
    const outer = lineIndent(node.start);
    const inner = lineIndent(node.properties[0].start);
    return inner.startsWith(outer) && inner.length > outer.length
      ? inner.slice(outer.length)
      : "  ";
  }

  function firstQuote(node) {
    let quote;
    walk(node);
    return quote;
    function walk(value) {
      if (quote || !value || typeof value.type !== "string") {
        return;
      }
      if (value.type === "Literal" && typeof value.value === "string") {
        quote = source[value.start];
        return;
      }
      for (const child of Object.values(value)) {
        for (const item of Array.isArray(child) ? child : [child]) {
          walk(item);
        }
      }
    }
  }

  function blocked(path, value, node) {
    const fullPath = [...path];
    while (isPlainObject(value) && Object.keys(value).length === 1) {
      const [key] = Object.keys(value);
      fullPath.push(key);
      value = value[key];
    }
    return {
      edit: {
        path: fullPath.join("."),
        currentExpression: source.slice(node.start, node.end),
        newValue: value,
      },
    };
  }
}

function merge(current, value) {
  if (!isPlainObject(current) || !isPlainObject(value)) {
    return value;
  }
  const result = { ...current };
  for (const [key, item] of Object.entries(value)) {
    result[key] = merge(result[key], item);
  }
  return result;
}

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function propertyName(property) {
  if (property.type !== "Property" || property.computed) {
    return undefined;
  }
  if (property.key.type === "Identifier") {
    return property.key.name;
  }
  return String(property.key.value);
}

// isLiteral reports whether node is data that the source spells out: a
// string, number, boolean, or null, or an array or object of them.
function isLiteral(node) {
  switch (node.type) {
    case "Literal":
      return !node.regex && !node.bigint;
    case "TemplateLiteral":
      return node.expressions.length === 0;
    case "UnaryExpression":
      return (
        node.operator === "-" &&
        node.argument.type === "Literal" &&
        typeof node.argument.value === "number"
      );
    case "ArrayExpression":
      return node.elements.every((element) => element && isLiteral(element));
    case "ObjectExpression":
      return node.properties.every(
        (property) =>
          property.type === "Property" &&
          property.kind === "init" &&
          !property.computed &&
          !property.method &&
          !property.shorthand &&
          isLiteral(property.value),
      );
  }
  return false;
}

function sameValue(node, value) {
  switch (node.type) {
    case "Literal":
      return node.value === value;
    case "TemplateLiteral":
      return node.quasis[0].value.cooked === value;
  }
  return false;
}

function propertyKey(key) {
  return /^[A-Za-z_$][\w$]*$/.test(key) ? key : JSON.stringify(key);
}

// render writes value as source text. An object on several lines puts each
// property on its own line, one indent unit deeper than indent.
function render(value, indent, lines, style) {
  if (typeof value === "string") {
    const text = JSON.stringify(value);
    if (style.quote !== "'") {
      return text;
    }
    return `'${text.slice(1, -1).replaceAll('\\"', '"').replaceAll("'", "\\'")}'`;
  }
  if (Array.isArray(value)) {
    return `[${value.map((item) => render(item, indent, false, style)).join(", ")}]`;
  }
  if (!isPlainObject(value)) {
    return JSON.stringify(value);
  }
  const entries = Object.entries(value);
  if (entries.length === 0) {
    return "{}";
  }
  if (!lines) {
    const body = entries.map(
      ([key, item]) =>
        `${propertyKey(key)}: ${render(item, indent, false, style)}`,
    );
    return `{ ${body.join(", ")} }`;
  }
  const inner = indent + style.unit;
  const body = entries.map(([key, item], index) => {
    const comma = index < entries.length - 1 || style.trailingComma ? "," : "";
    return `${style.newline}${inner}${propertyKey(key)}: ${render(item, inner, true, style)}${comma}`;
  });
  return `{${body.join("")}${style.newline}${indent}}`;
}
