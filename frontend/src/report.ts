export interface ReportItem {
  prosthesis_id: string;
  serial_number: string;
  period_start: string;
  events_count: number;
  avg_response_time_ms: number;
  p95_response_time_ms: number;
  avg_battery_percent: number;
  last_event_at: string;
}

export interface Report {
  user_id: string;
  from: string;
  to: string;
  processed_until: string;
  items: ReportItem[];
}

const csvColumns: Array<keyof ReportItem> = [
  'prosthesis_id',
  'serial_number',
  'period_start',
  'events_count',
  'avg_response_time_ms',
  'p95_response_time_ms',
  'avg_battery_percent',
  'last_event_at'
];

const escapeCsvValue = (value: string | number): string => {
  const text = String(value);
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
};

export const buildReportCsv = (report: Report): string => {
  const metadataColumns = ['user_id', 'report_from', 'report_to', 'processed_until'];
  const header = [...metadataColumns, ...csvColumns].join(',');
  const items: Array<Partial<ReportItem>> = report.items.length > 0 ? report.items : [{}];

  const rows = items.map((item) => {
    const metadata = [
      report.user_id,
      report.from,
      report.to,
      report.processed_until
    ];
    const metrics = csvColumns.map((column) => item[column] ?? '');

    return [...metadata, ...metrics].map(escapeCsvValue).join(',');
  });

  return `\uFEFF${[header, ...rows].join('\r\n')}\r\n`;
};

export const downloadReportCsv = (report: Report): void => {
  const blob = new Blob([buildReportCsv(report)], {
    type: 'text/csv;charset=utf-8'
  });
  const objectUrl = URL.createObjectURL(blob);
  const link = document.createElement('a');
  const reportDate = report.to.slice(0, 10);

  link.href = objectUrl;
  link.download = `bionicpro-report-${reportDate}.csv`;
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(objectUrl), 1000);
};
