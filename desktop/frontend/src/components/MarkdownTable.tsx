import { memo, useState, type ReactNode } from "react";
import type { VirtualMarkdownTableData } from "../lib/largeMarkdownTable";
import { useT } from "../lib/i18n";
import { RESOURCE_BUDGETS } from "../lib/resourceBudgets";

const TABLE_CELL_PAGE = RESOURCE_BUDGETS.markdownTableCellsPerPage;

export const MarkdownTable = memo(function MarkdownTable({ children }: { children?: ReactNode }) {
  return <div className="md-table-scroll"><table>{children}</table></div>;
});

/** Large plain tables retain the fast worker parse and page their DOM cells. */
export const MarkdownSourceTable = memo(function MarkdownSourceTable({ data }: { data: VirtualMarkdownTableData }) {
  const t = useT();
  const rowsPerPage = Math.max(20, Math.floor(TABLE_CELL_PAGE / Math.max(1, data.header.length)));
  const [visibleRows, setVisibleRows] = useState(rowsPerPage);
  const shown = data.rows.slice(0, visibleRows);
  return <div className="md-table-scroll" data-markdown-source-rows={data.rows.length}
    data-markdown-visible-rows={shown.length}><table>
    <thead><tr>{data.header.map((cell, index) => <th key={index} align={data.align[index] ?? undefined}>{cell}</th>)}</tr></thead>
    <tbody>{shown.map((row, index) => <tr key={index}>{row.map((cell, column) =>
      <td key={column} align={data.align[column] ?? undefined}>{cell}</td>)}</tr>)}</tbody>
  </table>{shown.length < data.rows.length && <button type="button" className="btn"
    onClick={() => setVisibleRows(count => count + rowsPerPage)}>{t("workspace.loadMore")}</button>}</div>;
});
