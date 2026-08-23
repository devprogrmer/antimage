import { useEffect, useState } from 'react';
import { t } from '../i18n';

interface DashboardMetrics {
  timestamp: number;
  active_users: number;
  total_subjects: number;
  nodes_online: number;
  nodes_total: number;
  traffic_today_gb: number;
  bandwidth_mbps: number;
  alerts_count: number;
  frozen_count: number;
  nodes: NodeMetric[];
}

interface NodeMetric {
  id: number;
  name: string;
  status: string;
  cpu_percent: number;
  ram_percent: number;
  user_count: number;
}

export function Dashboard() {
  const [metrics, setMetrics] = useState<DashboardMetrics | null>(null);
  const [connected, setConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const eventSource = new EventSource('/api/v1/dashboard/stream');

    eventSource.addEventListener('metrics', (event) => {
      try {
        const data = JSON.parse(event.data);
        setMetrics(data);
        setConnected(true);
        setError(null);
      } catch (err) {
        console.error('Failed to parse metrics:', err);
        setError('Failed to parse metrics data');
      }
    });

    eventSource.addEventListener('heartbeat', () => {
      setConnected(true);
    });

    eventSource.onerror = () => {
      setConnected(false);
      setError('Connection lost. Reconnecting...');
    };

    eventSource.onopen = () => {
      setConnected(true);
      setError(null);
    };

    return () => {
      eventSource.close();
    };
  }, []);

  if (error && !metrics) {
    return (
      <div className="p-8">
        <div className="bg-red-50 border border-red-200 rounded-lg p-4">
          <p className="text-red-800">{error}</p>
        </div>
      </div>
    );
  }

  if (!metrics) {
    return (
      <div className="p-8">
        <div className="animate-pulse">
          <div className="h-8 bg-gray-200 rounded w-1/4 mb-8"></div>
          <div className="grid grid-cols-4 gap-4 mb-8">
            {[1, 2, 3, 4].map((i) => (
              <div key={i} className="bg-gray-200 rounded-lg h-32"></div>
            ))}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="p-8">
      {/* Header */}
      <div className="flex items-center justify-between mb-8">
        <h1 className="text-3xl font-bold">{t('dashboard.title')}</h1>
        <div className="flex items-center space-x-2">
          <div
            className={`w-3 h-3 rounded-full ${
              connected ? 'bg-green-500' : 'bg-red-500'
            }`}
          ></div>
          <span className="text-sm text-gray-600">
            {connected ? t('dashboard.live') : t('dashboard.disconnected')}
          </span>
        </div>
      </div>

      {/* Metric Cards */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
        <MetricCard
          title={t('dashboard.active_users')}
          value={metrics.active_users}
          total={metrics.total_subjects}
          icon="👥"
          color="blue"
        />
        <MetricCard
          title={t('dashboard.nodes_online')}
          value={metrics.nodes_online}
          total={metrics.nodes_total}
          icon="🖥️"
          color="green"
        />
        <MetricCard
          title={t('dashboard.traffic_today')}
          value={`${metrics.traffic_today_gb.toFixed(2)} GB`}
          subtitle={`${metrics.bandwidth_mbps.toFixed(1)} Mbps`}
          icon="📊"
          color="purple"
        />
        <MetricCard
          title={t('dashboard.alerts')}
          value={metrics.alerts_count}
          subtitle={t('dashboard.frozen_count', { count: metrics.frozen_count })}
          icon="⚠️"
          color={metrics.alerts_count > 0 ? 'red' : 'gray'}
        />
      </div>

      {/* Nodes Grid */}
      <div className="bg-white rounded-lg shadow-sm border border-gray-200 p-6">
        <h2 className="text-xl font-semibold mb-4">{t('dashboard.nodes')}</h2>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {metrics.nodes.map((node) => (
            <NodeCard key={node.id} node={node} />
          ))}
        </div>
      </div>
    </div>
  );
}

interface MetricCardProps {
  title: string;
  value: string | number;
  total?: number;
  subtitle?: string;
  icon: string;
  color: 'blue' | 'green' | 'purple' | 'red' | 'gray';
}

function MetricCard({ title, value, total, subtitle, icon, color }: MetricCardProps) {
  const colorClasses = {
    blue: 'bg-blue-50 text-blue-600 border-blue-200',
    green: 'bg-green-50 text-green-600 border-green-200',
    purple: 'bg-purple-50 text-purple-600 border-purple-200',
    red: 'bg-red-50 text-red-600 border-red-200',
    gray: 'bg-gray-50 text-gray-600 border-gray-200',
  };

  return (
    <div className={`rounded-lg border p-6 ${colorClasses[color]}`}>
      <div className="flex items-center justify-between mb-2">
        <span className="text-sm font-medium opacity-75">{title}</span>
        <span className="text-2xl">{icon}</span>
      </div>
      <div className="text-3xl font-bold mb-1">
        {value}
        {total !== undefined && (
          <span className="text-lg font-normal opacity-75"> / {total}</span>
        )}
      </div>
      {subtitle && <div className="text-sm opacity-75">{subtitle}</div>}
    </div>
  );
}

interface NodeCardProps {
  node: NodeMetric;
}

function NodeCard({ node }: NodeCardProps) {
  const { t } = useTranslation();
  const statusColors = {
    online: 'bg-green-100 text-green-800 border-green-300',
    degraded: 'bg-yellow-100 text-yellow-800 border-yellow-300',
    offline: 'bg-red-100 text-red-800 border-red-300',
  };

  const statusColor = statusColors[node.status as keyof typeof statusColors] || statusColors.offline;

  return (
    <div className="bg-gray-50 rounded-lg border border-gray-200 p-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="font-semibold text-gray-900">{node.name}</h3>
        <span
          className={`px-2 py-1 rounded-full text-xs font-medium border ${statusColor}`}
        >
          {node.status}
        </span>
      </div>
      <div className="space-y-2">
        <div className="flex items-center justify-between text-sm">
          <span className="text-gray-600">{t('dashboard.users')}</span>
          <span className="font-medium">{node.user_count}</span>
        </div>
        {node.cpu_percent > 0 && (
          <div className="flex items-center justify-between text-sm">
            <span className="text-gray-600">{t('dashboard.cpu')}</span>
            <span className="font-medium">{node.cpu_percent.toFixed(1)}%</span>
          </div>
        )}
        {node.ram_percent > 0 && (
          <div className="flex items-center justify-between text-sm">
            <span className="text-gray-600">{t('dashboard.ram')}</span>
            <span className="font-medium">{node.ram_percent.toFixed(1)}%</span>
          </div>
        )}
      </div>
    </div>
  );
}
