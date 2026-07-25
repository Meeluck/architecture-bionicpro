import React, { useState } from 'react';
import { useKeycloak } from '@react-keycloak/web';
import { downloadReportCsv, Report } from '../report';

interface ApiErrorPayload {
  error?: {
    message?: string;
  };
  processed_until?: string;
}

const apiUrl = process.env.REACT_APP_API_URL?.replace(/\/$/, '');

const ReportPage: React.FC = () => {
  const { keycloak, initialized } = useKeycloak();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const downloadReport = async () => {
    if (!keycloak.authenticated) {
      setError('Для получения отчёта необходимо войти в систему.');
      return;
    }
    if (!apiUrl) {
      setError('Адрес сервиса отчётов не настроен.');
      return;
    }

    try {
      setLoading(true);
      setError(null);
      setSuccess(null);

      await keycloak.updateToken(30);
      if (!keycloak.token) {
        throw new Error('Не удалось обновить сессию. Войдите в систему ещё раз.');
      }

      const response = await fetch(`${apiUrl}/reports`, {
        headers: {
          Authorization: `Bearer ${keycloak.token}`,
          Accept: 'application/json'
        }
      });

      const payload = await response.json() as Report | ApiErrorPayload;
      if (!response.ok) {
        const apiError = payload as ApiErrorPayload;
        const processedUntil = apiError.processed_until
          ? ` Данные обработаны по ${new Date(apiError.processed_until).toLocaleString('ru-RU')}.`
          : '';
        throw new Error(
          `${apiError.error?.message || `Сервис отчётов вернул ошибку ${response.status}.`}${processedUntil}`
        );
      }

      const report = payload as Report;
      downloadReportCsv(report);
      setSuccess(
        `Отчёт скачан: ${report.items.length} строк. Данные обработаны по ${
          new Date(report.processed_until).toLocaleString('ru-RU')
        }.`
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Не удалось получить отчёт.');
    } finally {
      setLoading(false);
    }
  };

  if (!initialized) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-gray-100">
        <span className="text-gray-600">Загрузка…</span>
      </div>
    );
  }

  if (!keycloak.authenticated) {
    return (
      <div className="flex flex-col items-center justify-center min-h-screen bg-gray-100">
        <button
          onClick={() => keycloak.login()}
          className="px-4 py-2 bg-blue-500 text-white rounded hover:bg-blue-600"
        >
          Войти
        </button>
      </div>
    );
  }

  return (
    <div className="flex flex-col items-center justify-center min-h-screen bg-gray-100">
      <main className="w-full max-w-lg p-8 bg-white rounded-lg shadow-md">
        <div className="flex items-start justify-between gap-6 mb-6">
          <div>
            <h1 className="text-2xl font-bold">Отчёт о работе протеза</h1>
            <p className="mt-2 text-sm text-gray-600">
              Будет сформирован CSV-файл с данными только текущего пользователя.
            </p>
          </div>
          <button
            type="button"
            onClick={() => keycloak.logout()}
            className="text-sm text-gray-500 hover:text-gray-800"
          >
            Выйти
          </button>
        </div>

        <button
          type="button"
          onClick={downloadReport}
          disabled={loading}
          className={`w-full px-4 py-3 bg-blue-600 text-white font-medium rounded hover:bg-blue-700 ${
            loading ? 'opacity-50 cursor-not-allowed' : ''
          }`}
        >
          {loading ? 'Получаем отчёт…' : 'Скачать отчёт'}
        </button>

        {error && (
          <div role="alert" className="mt-4 p-4 bg-red-100 text-red-700 rounded">
            {error}
          </div>
        )}

        {success && (
          <div aria-live="polite" className="mt-4 p-4 bg-green-100 text-green-800 rounded">
            {success}
          </div>
        )}
      </main>
    </div>
  );
};

export default ReportPage;
