/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTablePagination,
  DataTableView,
  useDataTable,
} from '@/components/data-table'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber, formatPercent } from '@/lib/format'

import { getReferralPage } from '../api'
import { formatTimestamp } from '../lib/billing'
import type {
  ReferralInvitee,
  ReferralPermissions,
  ReferralRebate,
} from '../types'

type ReferralDetailsProps = {
  userId: number
  permissions: ReferralPermissions
}

export function ReferralDetails(props: ReferralDetailsProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const inviteeColumns = useMemo<ColumnDef<ReferralInvitee>[]>(
    () => [
      { accessorKey: 'username', header: t('Username') },
      {
        accessorKey: 'email',
        header: t('Email'),
        cell: ({ row }) => row.original.email || '—',
      },
    ],
    [t]
  )
  const rebateColumns = useMemo<ColumnDef<ReferralRebate>[]>(
    () => [
      {
        accessorKey: 'username',
        header: t('Username'),
        cell: ({ row }) => row.original.username || '—',
      },
      {
        accessorKey: 'record_type',
        header: t('Type'),
        cell: ({ row }) =>
          row.original.record_type === 'invitation_reward'
            ? t('Invitation rewards')
            : t('Referral rebates'),
      },
      {
        accessorKey: 'created_at',
        header: t('Date'),
        cell: ({ row }) => formatTimestamp(row.original.created_at),
      },
      {
        id: 'amount',
        header: t('Rebate basis'),
        cell: ({ row }) => {
          const record = row.original
          if (record.record_type === 'invitation_reward') return '—'
          if (record.redemption_quota) {
            return `${t('Redemption Code')} · ${formatQuotaWithCurrency(record.redemption_quota, { locale })}`
          }
          const fractionDigits = record.paid_amount.split('.')[1]?.length ?? 0
          return new Intl.NumberFormat(locale, {
            style: 'currency',
            currency: record.paid_currency,
            currencyDisplay: 'code',
            maximumFractionDigits: Math.max(2, fractionDigits),
          }).format(Number(record.paid_amount))
        },
      },
      {
        id: 'rate_bps',
        header: t('Rebate rate'),
        cell: ({ row }) =>
          row.original.record_type === 'invitation_reward'
            ? '—'
            : formatPercent(row.original.rate_bps / 100),
      },
      {
        id: 'rebate_quota',
        header: t('Amount'),
        cell: ({ row }) =>
          formatQuotaWithCurrency(row.original.rebate_quota, { locale }),
      },
      {
        id: 'status',
        header: t('Status'),
        cell: ({ row }) => {
          const rebate = row.original
          if (rebate.credited_at) return t('Credited to wallet')
          if (rebate.cancelled_at) return t('Cancelled')
          const remainingSeconds = rebate.remaining_seconds ?? 0
          if (remainingSeconds > 0) {
            const totalMinutes = Math.ceil(remainingSeconds / 60)
            const hours = Math.floor(totalMinutes / 60)
            const minutes = totalMinutes % 60
            const time = [
              hours > 0 ? `${formatNumber(hours, locale)}${t('h')}` : '',
              minutes > 0 ? `${formatNumber(minutes, locale)}${t('m')}` : '',
            ]
              .filter(Boolean)
              .join(' ')
            return t('Estimated credit in {{time}}', { time })
          }
          if (rebate.rebate_quota > 0 || rebate.exchange_rate_pending) {
            return t('Pending')
          }
          return '—'
        },
      },
    ],
    [locale, t]
  )

  return (
    <>
      {props.permissions.invitation_enabled && (
        <Card>
          <CardHeader>
            <CardTitle>{t('Invited Users')}</CardTitle>
          </CardHeader>
          <CardContent>
            <ReferralTable
              userId={props.userId}
              resource='invitees'
              columns={inviteeColumns}
              emptyTitle={t('No invited users yet')}
            />
          </CardContent>
        </Card>
      )}
      {(props.permissions.invitation_enabled ||
        props.permissions.rebate_enabled) && (
        <Card>
          <CardHeader>
            <CardTitle>{t('Referral earnings')}</CardTitle>
            {props.permissions.rebate_enabled && (
              <CardDescription>
                {t('Current rebate rate: {{rate}}', {
                  rate: formatPercent((props.permissions.rate_bps ?? 0) / 100),
                })}
              </CardDescription>
            )}
          </CardHeader>
          <CardContent>
            <ReferralTable
              userId={props.userId}
              resource='rebates'
              columns={rebateColumns}
              emptyTitle={t('No records')}
              shouldRefresh={(items) =>
                items.some(
                  (item) =>
                    !item.credited_at &&
                    !item.cancelled_at &&
                    (item.rebate_quota > 0 || item.exchange_rate_pending)
                )
              }
            />
          </CardContent>
        </Card>
      )}
    </>
  )
}

function ReferralTable<T extends { id: number; record_type?: string }>(props: {
  userId: number
  resource: 'invitees' | 'rebates'
  columns: ColumnDef<T>[]
  emptyTitle: string
  shouldRefresh?: (items: T[]) => boolean
}) {
  const { t } = useTranslation()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 10 })
  const result = useQuery({
    queryKey: ['referral', props.resource, props.userId, pagination],
    queryFn: () =>
      getReferralPage<T>(
        props.resource,
        pagination.pageIndex + 1,
        pagination.pageSize
      ),
    placeholderData: keepPreviousData,
    refetchInterval: (query) =>
      props.shouldRefresh?.(query.state.data?.items ?? []) ? 60_000 : false,
  })
  const { table } = useDataTable({
    data: result.data?.items ?? [],
    columns: props.columns,
    getRowId: (row) => `${row.record_type ?? props.resource}:${row.id}`,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    totalCount: result.data?.total ?? 0,
  })
  if (result.isError) {
    return <p role='alert'>{t('Failed to load referral records')}</p>
  }
  return (
    <div className='space-y-3'>
      <DataTableView
        table={table}
        isLoading={result.isPending}
        emptyTitle={props.emptyTitle}
      />
      {!!result.data?.total && <DataTablePagination table={table} compact />}
    </div>
  )
}
