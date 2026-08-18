import { File, FileText } from "lucide-react";
import "./filechip.css";

// A stored file, drawn as the small card a reader clicks to get it.
//
// Distinct from `Chip`, which is a FACT about a record and links off our
// origin: this is the file itself, so it is a same-origin download and its
// name is the filename the file was uploaded under — the name the saved copy
// will have. A row that carries paper should say WHICH paper, and two files on
// one row have to be tellable apart before the click, not after it.
//
// The glyph and the kind tag both name the kind and nothing more: they are
// decorative, and the filename between them is the accessible name.
export function FileChip({
  href,
  filename,
  size,
}: Readonly<{
  // Same-origin path to the bytes. This control is for OUR files; anything
  // pointing off the origin is a link, not a file.
  href: string;
  filename: string;
  // Already formatted for the reader's locale. A size is what tells a 400 KB
  // scan from a 40 MB one before the click; a file whose size was never
  // recorded simply says nothing rather than "0".
  size?: string;
}>) {
  const kind = fileKind(filename);
  const Glyph = kind === "PDF" ? FileText : File;
  return (
    <a className="file-chip" href={href} download={filename}>
      <Glyph size={14} aria-hidden="true" />
      {kind && (
        <span className="file-chip-kind" aria-hidden="true">
          {kind}
        </span>
      )}
      <span className="file-chip-name">{filename}</span>
      {size && <span className="file-chip-size">{size}</span>}
    </a>
  );
}

// The extension, upper-cased, or "" for a filename that carries none. Read
// from the name rather than guessed from the bytes: this is what the reader
// will see in their own downloads folder, and a tag that disagreed with it
// would be the control lying about the file it points at.
function fileKind(filename: string): string {
  const dot = filename.lastIndexOf(".");
  const extension = dot > 0 ? filename.slice(dot + 1) : "";
  return extension.length > 0 && extension.length <= 4
    ? extension.toUpperCase()
    : "";
}
