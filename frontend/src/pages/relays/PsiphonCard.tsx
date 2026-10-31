import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Col,
  Form,
  Input,
  Popconfirm,
  Progress,
  Row,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd';
import {
  CloudDownloadOutlined,
  DashboardOutlined,
  LockOutlined,
  RadarChartOutlined,
  ReloadOutlined,
} from '@ant-design/icons';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import { FormField, rhfZodValidate } from '@/components/form/rhf';
import { usePsiphonInbounds, usePsiphonMutations, usePsiphonState } from '@/api/queries/usePsiphon';
import {
  PSIPHON_MB_PER_COUNTRY,
  PSIPHON_WARP_MODES,
  PsiphonWarpFormSchema,
  psiphonCountryLabel,
  psiphonExitWorks,
  psiphonPhase,
  type PsiphonCountry,
  type PsiphonCountryUse,
  type PsiphonExitTest,
  type PsiphonInbound,
  type PsiphonPhase,
  type PsiphonState,
  type PsiphonWarp,
  type PsiphonWarpFormValues,
} from '@/schemas/psiphon';
import { IntlUtil, LanguageManager } from '@/utils';
import { bytes } from './bytes';

type PsiphonMutations = ReturnType<typeof usePsiphonMutations>;

const PHASE_COLOR: Record<PsiphonPhase, string | undefined> = {
  off: undefined,
  connecting: 'processing',
  connected: 'success',
  error: 'error',
};

function phaseTag(country: PsiphonCountry | undefined, t: TFunction) {
  const phase = psiphonPhase(country);
  return <Tag color={PHASE_COLOR[phase]}>{t(`pages.relays.psiphon.phase.${phase}`)}</Tag>;
}

function clipped(text: string, type: 'danger' | 'warning', max = 48) {
  return (
    <Tooltip title={text}>
      <Typography.Text type={type} style={{ fontSize: 12, cursor: 'help' }}>
        {text.length > max ? `${text.slice(0, max)}…` : text}
      </Typography.Text>
    </Tooltip>
  );
}

// Where the country's traffic last came out, read through its own SOCKS port,
// and why the last check failed when it did.
function countryDetail(country: PsiphonCountry | undefined) {
  if (!country?.enabled) return null;
  const phase = psiphonPhase(country);
  return (
    <>
      {phase === 'connected' && country.exitIp ? (
        <span style={{ fontSize: 12, opacity: 0.7 }}>
          <bdi>{country.exitIp}</bdi>
          {country.exitLoc ? ` · ${country.exitLoc}` : ''}
        </span>
      ) : null}
      {country.lastError
        ? clipped(country.lastError, phase === 'error' ? 'danger' : 'warning')
        : null}
    </>
  );
}

function measuredText(test: PsiphonExitTest, t: TFunction): string {
  return t('pages.relays.psiphon.check.measured', {
    latency: test.latencyMs,
    speed: bytes(test.speed),
  });
}

// How long ago a test ran, in the panel's language; the card re-renders it as time passes.
function testAge(at: number, now: number, t: TFunction): string {
  const secs = Math.max(0, Math.round(now / 1000 - at));
  if (secs < 60) return t('pages.relays.psiphon.check.justNow');
  const fmt = new Intl.RelativeTimeFormat(LanguageManager.getLanguage(), { numeric: 'auto' });
  if (secs < 3600) return fmt.format(-Math.floor(secs / 60), 'minute');
  if (secs < 86400) return fmt.format(-Math.floor(secs / 3600), 'hour');
  return fmt.format(-Math.floor(secs / 86400), 'day');
}

const AGE_TICK_MS = 15_000;

function useNow(everyMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), everyMs);
    return () => clearInterval(timer);
  }, [everyMs]);
  return now;
}

const CHANGED_MS = 4000;

function measuredDiffers(a: PsiphonExitTest, b: PsiphonExitTest): boolean {
  return (
    a.latencyMs !== b.latencyMs ||
    a.speed !== b.speed ||
    a.exitIp !== b.exitIp ||
    a.error !== b.error
  );
}

function testsByCode(countries: PsiphonCountry[]): Map<string, PsiphonExitTest> {
  return new Map(countries.flatMap((c) => (c.test ? [[c.code, c.test] as const] : [])));
}

// The countries whose numbers a test just changed, for a few seconds after the new
// result lands; a country's first result is news, not a change.
function useJustChanged(countries: PsiphonCountry[]): Set<string> {
  const [seen, setSeen] = useState(() => testsByCode(countries));
  const [changed, setChanged] = useState<Set<string>>(() => new Set());
  const tests = testsByCode(countries);
  const landed = [...tests].filter(([code, test]) => seen.get(code)?.at !== test.at);
  if (landed.length) {
    setSeen(tests);
    const moved = landed.filter(([code, test]) => {
      const before = seen.get(code);
      return before !== undefined && measuredDiffers(before, test);
    });
    if (moved.length) setChanged(new Set(moved.map(([code]) => code)));
  }
  useEffect(() => {
    if (!changed.size) return;
    const timer = setTimeout(() => setChanged(new Set()), CHANGED_MS);
    return () => clearTimeout(timer);
  }, [changed]);
  return changed;
}

// The card's last test of a running country: its round trip and download speed, or why
// it failed, how long ago underneath; a spinner instead while a test is under way.
function CountryTest({
  country,
  testing,
  changed,
  now,
}: {
  country: PsiphonCountry | undefined;
  testing: boolean;
  changed: boolean;
  now: number;
}) {
  const { t } = useTranslation();
  const { token } = theme.useToken();
  if (!country?.enabled) return null;
  const { test } = country;
  let result = null;
  if (test) {
    result = psiphonExitWorks(test) ? (
      <span style={{ fontSize: 12, opacity: 0.7 }}>{measuredText(test, t)}</span>
    ) : (
      clipped(test.error || t('pages.relays.psiphon.check.noTest'), 'danger')
    );
  }
  const age = test ? (
    <Tooltip
      title={t('pages.relays.psiphon.check.testedAt', {
        time: IntlUtil.formatDate(test.at * 1000),
      })}
    >
      <span style={{ fontSize: 12, opacity: 0.6, cursor: 'help' }}>
        {t('pages.relays.psiphon.check.testedAgo', { age: testAge(test.at, now, t) })}
      </span>
    </Tooltip>
  ) : null;
  return (
    <>
      {result ? (
        <span
          className={changed ? 'psiphon-test-changed' : undefined}
          style={{
            borderRadius: 4,
            transition: 'background-color 0.6s',
            background: changed ? token.colorWarningBg : 'transparent',
          }}
        >
          {result}
        </span>
      ) : null}
      {testing && country.running ? (
        <Space size={4} style={{ fontSize: 12 }}>
          <Spin size="small" />
          <span>{t('pages.relays.psiphon.check.testing')}</span>
        </Space>
      ) : (
        age
      )}
    </>
  );
}

function trafficText(down: number, up: number): string {
  return `↓ ${bytes(down)} · ↑ ${bytes(up)}`;
}

// What customers moved through the country since the last reset; a country
// switched off keeps its count, so it shows here too.
function countryTraffic(country: PsiphonCountry | undefined) {
  if (!country || (!country.trafficDown && !country.trafficUp)) return null;
  return (
    <span style={{ fontSize: 12, opacity: 0.7 }}>
      {trafficText(country.trafficDown, country.trafficUp)}
    </span>
  );
}

function TrafficTotal({
  countries,
  mutations,
}: {
  countries: PsiphonCountry[];
  mutations: PsiphonMutations;
}) {
  const { t } = useTranslation();
  const { resetTrafficMut } = mutations;
  const down = countries.reduce((sum, c) => sum + c.trafficDown, 0);
  const up = countries.reduce((sum, c) => sum + c.trafficUp, 0);
  return (
    <Space wrap style={{ display: 'flex', marginBottom: 12 }}>
      <Tooltip title={t('pages.relays.psiphon.traffic.hint')}>
        <span style={{ cursor: 'help' }}>
          {t('pages.relays.psiphon.traffic.total', { traffic: trafficText(down, up) })}
        </span>
      </Tooltip>
      <Popconfirm
        title={t('pages.relays.psiphon.traffic.resetConfirm')}
        description={t('pages.relays.psiphon.traffic.resetHint')}
        okType="danger"
        okText={t('reset')}
        cancelText={t('cancel')}
        onConfirm={() => resetTrafficMut.mutateAsync({ all: true })}
      >
        <Button size="small" loading={resetTrafficMut.isPending}>
          {t('pages.relays.psiphon.traffic.reset')}
        </Button>
      </Popconfirm>
    </Space>
  );
}

// A restore or a promoted copy brings the countries back but not the client: say they are
// down until the panel's own download lands, rather than look as if none was ever set up.
function MissingClientAlert({ state }: { state: PsiphonState }) {
  const { t } = useTranslation();
  const enabled = state.countries.filter((c) => c.enabled);
  if (!enabled.length) return null;
  return (
    <Alert
      type="warning"
      showIcon
      title={t('pages.relays.psiphon.missingClient', {
        countries: enabled.map((c) => psiphonCountryLabel(c.code, c)).join(', '),
      })}
    />
  );
}

function installTag(state: PsiphonState, t: TFunction) {
  if (!state.supported) {
    return <Tag color="warning">{t('pages.relays.psiphon.status.unsupported')}</Tag>;
  }
  if (!state.installed) return <Tag>{t('pages.relays.psiphon.status.notInstalled')}</Tag>;
  const enabled = state.countries.filter((c) => c.enabled);
  const connected = enabled.filter((c) => c.connected).length;
  return (
    <Space size={4} wrap>
      <Tag color="success">{t('pages.relays.psiphon.status.installed')}</Tag>
      {enabled.length ? (
        <Tag color={connected === enabled.length ? 'success' : 'processing'}>
          {t('pages.relays.psiphon.status.connected', { connected, enabled: enabled.length })}
        </Tag>
      ) : null}
    </Space>
  );
}

function enabledCodesOf(state: PsiphonState): string[] {
  return state.countries
    .filter((c) => c.enabled)
    .map((c) => c.code)
    .sort();
}

function whereUsed(use: PsiphonCountryUse, t: TFunction): string {
  const { inbound, count, location } = use;
  if (use.kind === 'host') {
    return count === 1
      ? t('pages.relays.psiphon.countries.useHost', { inbound })
      : t('pages.relays.psiphon.countries.useHosts', { inbound, count });
  }
  if (use.kind === 'cisco') {
    return location
      ? t('pages.relays.psiphon.countries.useCiscoLocation', { inbound, location })
      : t('pages.relays.psiphon.countries.useCisco', { inbound });
  }
  return t('pages.relays.psiphon.countries.useOpenvpn', { inbound });
}

// A country still leaving customers somewhere: locked while on, and named as broken
// when it is off all the same, with exactly where in the tooltip.
function inUseTag(country: PsiphonCountry | undefined, t: TFunction) {
  if (!country?.usedBy.length) return null;
  const uses = country.usedBy.map((u) => whereUsed(u, t)).join(' · ');
  const on = country.enabled;
  return (
    <Tooltip title={t(`pages.relays.psiphon.countries.${on ? 'inUseHint' : 'inUseOff'}`, { uses })}>
      <Tag
        color={on ? 'gold' : 'error'}
        icon={<LockOutlined aria-hidden />}
        style={{ cursor: 'help', marginInlineEnd: 0 }}
      >
        {t('pages.relays.psiphon.countries.inUse')}
      </Tag>
    </Tooltip>
  );
}

function CountriesSection({
  state,
  mutations,
}: {
  state: PsiphonState;
  mutations: PsiphonMutations;
}) {
  const { t } = useTranslation();
  const { refreshRegionsMut, saveCountriesMut, testMut } = mutations;
  const byCode = useMemo(() => new Map(state.countries.map((c) => [c.code, c])), [state.countries]);
  // A country still enabled stays listed when a refresh no longer offers it,
  // so it can still be switched off.
  const codes = useMemo(() => {
    const dropped = state.countries
      .filter((c) => c.enabled && !state.regions.includes(c.code))
      .map((c) => c.code);
    return [...state.regions, ...dropped];
  }, [state.regions, state.countries]);

  const enabledSig = enabledCodesOf(state).join(',');
  const methods = useForm<{ codes: string[] }>({
    defaultValues: { codes: enabledSig ? enabledSig.split(',') : [] },
  });
  // The state is polled; a fresh copy only replaces the ticks while the
  // operator has not started changing them.
  const { isDirty } = methods.formState;
  useEffect(() => {
    if (!isDirty) methods.reset({ codes: enabledSig ? enabledSig.split(',') : [] });
  }, [enabledSig, isDirty, methods]);
  const picked = useWatch({ control: methods.control, name: 'codes' }) ?? [];
  const now = useNow(AGE_TICK_MS);
  const changed = useJustChanged(state.countries);
  const [refusal, setRefusal] = useState('');
  const locked = (country: PsiphonCountry | undefined) =>
    Boolean(country?.enabled && country.usedBy.length);

  // A refused save puts the ticks back on what runs, and says why beside the button.
  const onSave = methods.handleSubmit(async ({ codes: next }) => {
    const msg = await saveCountriesMut.mutateAsync(next);
    setRefusal(msg?.success ? '' : msg?.msg || t('somethingWentWrong'));
    methods.reset({ codes: msg?.success ? next : enabledSig ? enabledSig.split(',') : [] });
  });

  return (
    <Card
      size="small"
      title={t('pages.relays.psiphon.countries.title')}
      extra={
        <Button
          size="small"
          icon={<ReloadOutlined aria-hidden />}
          loading={refreshRegionsMut.isPending}
          onClick={() => refreshRegionsMut.mutate()}
        >
          {t('pages.relays.psiphon.countries.refresh')}
        </Button>
      }
    >
      <Typography.Paragraph type="secondary">
        {t('pages.relays.psiphon.countries.hint')}
      </Typography.Paragraph>
      <FormProvider {...methods}>
        <Form layout="vertical">
          <FormField
            name="codes"
            onAfterChange={() => setRefusal('')}
            extra={
              state.regionsAt
                ? t('pages.relays.psiphon.countries.listedAt', {
                    time: IntlUtil.formatDate(state.regionsAt * 1000),
                  })
                : undefined
            }
          >
            <Checkbox.Group style={{ width: '100%' }}>
              <Row gutter={[12, 8]} style={{ width: '100%' }}>
                {codes.map((code) => {
                  const country = byCode.get(code);
                  return (
                    <Col key={code} xs={24} sm={12} lg={8} xl={6}>
                      <Space orientation="vertical" size={0}>
                        <Space size={4} wrap>
                          <Checkbox value={code} disabled={locked(country)}>
                            {psiphonCountryLabel(code, country)}
                          </Checkbox>
                          {phaseTag(country, t)}
                          {inUseTag(country, t)}
                        </Space>
                        {countryDetail(country)}
                        <CountryTest
                          country={country}
                          testing={testMut.isPending}
                          changed={changed.has(code)}
                          now={now}
                        />
                        {countryTraffic(country)}
                      </Space>
                    </Col>
                  );
                })}
              </Row>
            </Checkbox.Group>
          </FormField>
          <TrafficTotal countries={state.countries} mutations={mutations} />
          <Space wrap style={{ marginBottom: refusal ? 8 : 0 }}>
            <Button
              type="primary"
              disabled={!isDirty}
              loading={saveCountriesMut.isPending}
              onClick={onSave}
            >
              {t('pages.relays.psiphon.countries.save')}
            </Button>
            <span style={{ fontSize: 12, opacity: 0.7 }}>
              {t('pages.relays.psiphon.countries.memory', {
                count: picked.length,
                mb: picked.length * PSIPHON_MB_PER_COUNTRY,
              })}
            </span>
          </Space>
          {refusal ? <Alert type="error" showIcon title={refusal} /> : null}
        </Form>
      </FormProvider>
    </Card>
  );
}

// Three temporary clients run at once while a scan goes, each about one country's worth.
const SCAN_PARALLEL = 3;

function resultTag(test: PsiphonExitTest, t: TFunction) {
  if (psiphonExitWorks(test)) {
    return <Tag color="success">{t('pages.relays.psiphon.check.works')}</Tag>;
  }
  const tag = test.connected ? (
    <Tag color="warning">{t('pages.relays.psiphon.check.noTest')}</Tag>
  ) : (
    <Tag>{t('pages.relays.psiphon.check.noConnect')}</Tag>
  );
  return test.error ? (
    <Tooltip title={test.error}>
      <span style={{ cursor: 'help' }}>{tag}</span>
    </Tooltip>
  ) : (
    tag
  );
}

function scanSummary(state: PsiphonState, t: TFunction): string | null {
  const { scan } = state;
  const done = scan.results.length;
  if (scan.running) return t('pages.relays.psiphon.check.scanning', { done, total: scan.total });
  if (!scan.startedAt) return null;
  if (scan.stopped) return t('pages.relays.psiphon.check.stopped', { done, total: scan.total });
  return t('pages.relays.psiphon.check.scanned', {
    time: IntlUtil.formatDate(scan.finishedAt * 1000),
    works: scan.results.filter(psiphonExitWorks).length,
    total: scan.total,
  });
}

// Test measures the running countries through their own clients; scan tries every
// country with a temporary client, and a working one is switched on from its row.
function CheckSection({ state, mutations }: { state: PsiphonState; mutations: PsiphonMutations }) {
  const { t } = useTranslation();
  const { testMut, scanMut, saveCountriesMut } = mutations;
  const [enabling, setEnabling] = useState('');
  const byCode = useMemo(() => new Map(state.countries.map((c) => [c.code, c])), [state.countries]);
  const enabledCodes = enabledCodesOf(state);
  const { scan } = state;
  const summary = scanSummary(state, t);

  const onEnable = async (code: string) => {
    setEnabling(code);
    try {
      await saveCountriesMut.mutateAsync([...enabledCodes, code]);
    } finally {
      setEnabling('');
    }
  };

  const columns = [
    {
      title: t('pages.relays.psiphon.check.country'),
      key: 'country',
      render: (_: unknown, row: PsiphonExitTest) =>
        psiphonCountryLabel(row.code, byCode.get(row.code)),
    },
    {
      title: t('pages.relays.psiphon.check.result'),
      key: 'result',
      render: (_: unknown, row: PsiphonExitTest) => resultTag(row, t),
    },
    {
      title: t('pages.relays.psiphon.check.exit'),
      key: 'exit',
      render: (_: unknown, row: PsiphonExitTest) =>
        row.exitIp ? (
          <span>
            <bdi>{row.exitIp}</bdi>
            {row.exitLoc ? ` · ${row.exitLoc}` : ''}
          </span>
        ) : null,
    },
    {
      title: t('pages.relays.psiphon.check.latency'),
      key: 'latency',
      sorter: (a: PsiphonExitTest, b: PsiphonExitTest) =>
        (a.latencyMs || Infinity) - (b.latencyMs || Infinity),
      render: (_: unknown, row: PsiphonExitTest) => (row.latencyMs ? `${row.latencyMs} ms` : null),
    },
    {
      title: t('pages.relays.psiphon.check.speed'),
      key: 'speed',
      sorter: (a: PsiphonExitTest, b: PsiphonExitTest) => a.speed - b.speed,
      render: (_: unknown, row: PsiphonExitTest) => (row.speed ? `${bytes(row.speed)}/s` : null),
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, row: PsiphonExitTest) => {
        if (enabledCodes.includes(row.code)) {
          return <Tag color="blue">{t('pages.relays.psiphon.check.enabled')}</Tag>;
        }
        if (!psiphonExitWorks(row)) return null;
        return (
          <Button
            size="small"
            loading={enabling === row.code}
            disabled={saveCountriesMut.isPending && enabling !== row.code}
            onClick={() => onEnable(row.code)}
          >
            {t('pages.relays.psiphon.check.enable')}
          </Button>
        );
      },
    },
  ];

  return (
    <Card size="small" title={t('pages.relays.psiphon.check.title')}>
      <Typography.Paragraph type="secondary">
        {t('pages.relays.psiphon.check.hint', { mb: SCAN_PARALLEL * PSIPHON_MB_PER_COUNTRY })}
      </Typography.Paragraph>
      <Space wrap style={{ marginBottom: 12 }}>
        <Button
          icon={<DashboardOutlined aria-hidden />}
          disabled={!state.countries.some((c) => c.enabled && c.running)}
          loading={testMut.isPending}
          onClick={() => testMut.mutate()}
        >
          {t('pages.relays.psiphon.check.test')}
        </Button>
        <Button
          icon={<RadarChartOutlined aria-hidden />}
          disabled={scan.running}
          loading={scanMut.isPending}
          onClick={() => scanMut.mutate()}
        >
          {t('pages.relays.psiphon.check.scan')}
        </Button>
      </Space>
      {summary ? (
        <Typography.Paragraph type={scan.stopped && !scan.running ? 'warning' : undefined}>
          {summary}
        </Typography.Paragraph>
      ) : null}
      {scan.running ? (
        <Progress
          percent={scan.total ? Math.round((scan.results.length / scan.total) * 100) : 0}
          status="active"
          style={{ marginBottom: 12 }}
        />
      ) : null}
      {scan.results.length ? (
        <Table
          rowKey="code"
          size="small"
          dataSource={scan.results}
          columns={columns}
          pagination={false}
          scroll={{ x: 'max-content' }}
        />
      ) : null}
    </Card>
  );
}

function sameCodes(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((code) => b.includes(code));
}

function InboundsSection({
  state,
  inbounds,
  loading,
  error,
  mutations,
}: {
  state: PsiphonState;
  inbounds: PsiphonInbound[];
  loading: boolean;
  error: unknown;
  mutations: PsiphonMutations;
}) {
  const { t } = useTranslation();
  const { saveInboundMut } = mutations;
  // Picks not saved yet, per inbound; a row without one shows what it carries.
  const [drafts, setDrafts] = useState<Record<number, string[]>>({});
  const byCode = useMemo(() => new Map(state.countries.map((c) => [c.code, c])), [state.countries]);
  const enabledCodes = enabledCodesOf(state);

  const optionsFor = (row: PsiphonInbound) =>
    [...new Set([...enabledCodes, ...row.countries])].map((code) => ({
      value: code,
      label: psiphonCountryLabel(code, byCode.get(code)),
    }));

  const onSaveRow = async (row: PsiphonInbound) => {
    const codes = drafts[row.inboundId] ?? row.countries;
    const msg = await saveInboundMut.mutateAsync({ inboundId: row.inboundId, codes });
    if (msg?.success) {
      setDrafts((was) => {
        const next = { ...was };
        delete next[row.inboundId];
        return next;
      });
    }
  };

  const columns = [
    {
      title: t('pages.relays.psiphon.inbounds.inbound'),
      key: 'inbound',
      render: (_: unknown, row: PsiphonInbound) => (
        <Space orientation="vertical" size={0}>
          <strong>{row.remark || `#${row.inboundId}`}</strong>
          <Tag style={{ marginTop: 2 }}>{row.protocol}</Tag>
        </Space>
      ),
    },
    {
      title: t('pages.relays.psiphon.inbounds.countries'),
      key: 'countries',
      render: (_: unknown, row: PsiphonInbound) => {
        const value = drafts[row.inboundId] ?? row.countries;
        const onChange = (next: string | string[] | undefined) =>
          setDrafts((was) => ({
            ...was,
            [row.inboundId]: Array.isArray(next) ? next : next ? [next] : [],
          }));
        // WireGuard has no route in its packets to tell countries apart, so the
        // whole inbound leaves through the one it is given.
        return row.protocol === 'wireguard' ? (
          <Select
            allowClear
            aria-label={t('pages.relays.psiphon.inbounds.countries')}
            style={{ minWidth: 240 }}
            value={value[0]}
            options={optionsFor(row)}
            placeholder={t('pages.relays.psiphon.inbounds.pickOne')}
            onChange={onChange}
          />
        ) : (
          <Select
            mode="multiple"
            allowClear
            aria-label={t('pages.relays.psiphon.inbounds.countries')}
            style={{ minWidth: 240 }}
            value={value}
            options={optionsFor(row)}
            placeholder={t('pages.relays.psiphon.inbounds.pick')}
            onChange={onChange}
          />
        );
      },
    },
    {
      title: '',
      key: 'actions',
      render: (_: unknown, row: PsiphonInbound) => {
        const draft = drafts[row.inboundId];
        return (
          <Button
            size="small"
            type="primary"
            disabled={draft === undefined || sameCodes(draft, row.countries)}
            loading={
              saveInboundMut.isPending && saveInboundMut.variables?.inboundId === row.inboundId
            }
            onClick={() => onSaveRow(row)}
          >
            {t('pages.relays.psiphon.inbounds.save')}
          </Button>
        );
      },
    },
  ];

  return (
    <Card size="small" title={t('pages.relays.psiphon.inbounds.title')}>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        title={t('pages.relays.psiphon.inbounds.hint')}
        description={t('pages.relays.psiphon.inbounds.wireguardHint')}
      />
      {enabledCodes.length === 0 ? (
        <Typography.Paragraph type="warning">
          {t('pages.relays.psiphon.inbounds.noCountries')}
        </Typography.Paragraph>
      ) : null}
      {error ? (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          title={error instanceof Error ? error.message : String(error)}
        />
      ) : null}
      <Table
        rowKey="inboundId"
        size="small"
        loading={loading}
        dataSource={inbounds}
        columns={columns}
        pagination={false}
        scroll={{ x: 'max-content' }}
        locale={{ emptyText: t('pages.relays.psiphon.inbounds.empty') }}
      />
    </Card>
  );
}

function warpFormValuesOf(warp: PsiphonWarp): PsiphonWarpFormValues {
  return { mode: warp.mode, sites: warp.sites };
}

function WarpSection({ warp, mutations }: { warp: PsiphonWarp; mutations: PsiphonMutations }) {
  const { t } = useTranslation();
  const { saveWarpMut } = mutations;
  const methods = useForm<PsiphonWarpFormValues>({ defaultValues: warpFormValuesOf(warp) });
  const { isDirty } = methods.formState;
  const serverSig = JSON.stringify(warpFormValuesOf(warp));
  useEffect(() => {
    if (!isDirty) methods.reset(JSON.parse(serverSig) as PsiphonWarpFormValues);
  }, [serverSig, isDirty, methods]);
  const mode = useWatch({ control: methods.control, name: 'mode' });

  const onSave = methods.handleSubmit(async (values) => {
    const msg = await saveWarpMut.mutateAsync(values);
    if (msg?.success) methods.reset(values);
  });

  return (
    <Card
      size="small"
      title={t('pages.relays.psiphon.warp.title')}
      extra={
        warp.registered ? (
          <Tag color="success">{t('pages.relays.psiphon.warp.registered')}</Tag>
        ) : null
      }
    >
      <Typography.Paragraph type="secondary">
        {t('pages.relays.psiphon.warp.hint')}
      </Typography.Paragraph>
      <FormProvider {...methods}>
        <Form layout="vertical">
          <FormField name="mode" label={t('pages.relays.psiphon.warp.mode')}>
            <Select
              style={{ maxWidth: 320 }}
              options={PSIPHON_WARP_MODES.map((m) => ({
                value: m,
                label: t(`pages.relays.psiphon.warp.modes.${m}`),
              }))}
            />
          </FormField>
          {mode === 'sites' ? (
            <FormField
              name="sites"
              label={t('pages.relays.psiphon.warp.sites')}
              extra={t('pages.relays.psiphon.warp.sitesHint')}
              rules={{ validate: rhfZodValidate(PsiphonWarpFormSchema.shape.sites) }}
            >
              <Input.TextArea
                rows={6}
                dir="ltr"
                spellCheck={false}
                placeholder={'domain:gemini.google.com\ndomain:chatgpt.com'}
              />
            </FormField>
          ) : null}
          {mode !== 'off' && !warp.registered ? (
            <Typography.Paragraph type="secondary">
              {t('pages.relays.psiphon.warp.registerOnSave')}
            </Typography.Paragraph>
          ) : null}
          <Button
            type="primary"
            disabled={!isDirty}
            loading={saveWarpMut.isPending}
            onClick={onSave}
          >
            {t('pages.relays.psiphon.warp.save')}
          </Button>
        </Form>
      </FormProvider>
    </Card>
  );
}

// Psiphon on this panel's own server: install it, pick the countries it runs,
// give them to inbounds, and put WARP behind them for the sites that refuse it.
export default function PsiphonCard() {
  const { t } = useTranslation();
  const stateQuery = usePsiphonState();
  const state = stateQuery.data;
  const ready = Boolean(state?.supported && state.installed);
  const inboundsQuery = usePsiphonInbounds(ready);
  const mutations = usePsiphonMutations();
  const { installMut } = mutations;

  return (
    <Card
      size="small"
      title={t('pages.relays.psiphon.title')}
      extra={state ? installTag(state, t) : null}
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Alert
          type="info"
          showIcon
          title={t('pages.relays.psiphon.intro')}
          description={t('pages.relays.psiphon.introHint')}
        />

        {stateQuery.error ? (
          <Alert
            type="error"
            showIcon
            title={t('somethingWentWrong')}
            description={
              stateQuery.error instanceof Error
                ? stateQuery.error.message
                : String(stateQuery.error)
            }
            action={
              <Button
                size="small"
                loading={stateQuery.isFetching}
                onClick={() => stateQuery.refetch()}
              >
                {t('refresh')}
              </Button>
            }
          />
        ) : null}

        {!state && stateQuery.isLoading ? (
          <div style={{ textAlign: 'center', padding: 24 }}>
            <Spin />
          </div>
        ) : null}

        {state && !state.supported ? (
          <Alert
            type="warning"
            showIcon
            title={t('pages.relays.psiphon.unsupported')}
            description={t('pages.relays.psiphon.unsupportedHint')}
          />
        ) : null}

        {state?.supported && !state.installed ? (
          <Space orientation="vertical" size={8}>
            <MissingClientAlert state={state} />
            <Typography.Text>
              {t('pages.relays.psiphon.installHint', { mb: PSIPHON_MB_PER_COUNTRY })}
            </Typography.Text>
            <Space wrap>
              <Button
                type="primary"
                icon={<CloudDownloadOutlined aria-hidden />}
                loading={installMut.isPending}
                onClick={() => installMut.mutate()}
              >
                {t('pages.relays.psiphon.install')}
              </Button>
              {installMut.isPending ? (
                <Typography.Text type="secondary">
                  {t('pages.relays.psiphon.installing')}
                </Typography.Text>
              ) : null}
            </Space>
          </Space>
        ) : null}

        {state && ready ? (
          <>
            <CountriesSection state={state} mutations={mutations} />
            <CheckSection state={state} mutations={mutations} />
            <InboundsSection
              state={state}
              inbounds={inboundsQuery.data ?? []}
              loading={inboundsQuery.isLoading}
              error={inboundsQuery.error}
              mutations={mutations}
            />
            <WarpSection warp={state.warp} mutations={mutations} />
          </>
        ) : null}
      </Space>
    </Card>
  );
}
