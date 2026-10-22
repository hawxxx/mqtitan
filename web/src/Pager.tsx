import { number } from "./model";

// Pager is the shared "1–20 of 45 · Previous · 1 / 3 · Next" footer. page is
// zero-based and is clamped by the caller's slice.
export function Pager({
  label,
  total,
  page,
  pageSize,
  onPage,
}: {
  label: string;
  total: number;
  page: number;
  pageSize: number;
  onPage: (page: number) => void;
}) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const current = Math.min(page, pages - 1);
  const first = current * pageSize;
  const shown = Math.max(0, Math.min(pageSize, total - first));
  return (
    <nav className="balance-pager" aria-label={`${label} pages`}>
      <span>
        {total
          ? `${number(first + 1)}–${number(first + shown)} of ${number(total)}`
          : "No matches"}
      </span>
      <button disabled={current === 0} onClick={() => onPage(current - 1)}>
        Previous
      </button>
      <span className="mono">
        {current + 1} / {pages}
      </span>
      <button
        disabled={current >= pages - 1}
        onClick={() => onPage(current + 1)}
      >
        Next
      </button>
    </nav>
  );
}
