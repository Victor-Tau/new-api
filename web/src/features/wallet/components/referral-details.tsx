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
import { formatPercent } from '@/lib/format'

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
      { accessorKey: 'username', header: t('Username') },
      {
        accessorKey: 'created_at',
        header: t('Date'),
        cell: ({ row }) => formatTimestamp(row.original.created_at),
      },
      {
        id: 'paid_amount',
        header: t('Actual payment'),
        cell: ({ row }) => {
          const record = row.original
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
        cell: ({ row }) => formatPercent(row.original.rate_bps / 100),
      },
      {
        id: 'rebate_quota',
        header: t('Rebate amount'),
        cell: ({ row }) =>
          formatQuotaWithCurrency(row.original.rebate_quota, { locale }),
      },
      {
        id: 'status',
        header: t('Status'),
        cell: ({ row }) => {
          if (row.original.credited_at) return t('Credited to wallet')
          if (row.original.rebate_quota || row.original.exchange_rate_pending) {
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
      {props.permissions.rebate_enabled && (
        <Card>
          <CardHeader>
            <CardTitle>{t('Referral rebates')}</CardTitle>
            <CardDescription>
              {t('Current rebate rate: {{rate}}', {
                rate: formatPercent((props.permissions.rate_bps ?? 0) / 100),
              })}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ReferralTable
              userId={props.userId}
              resource='rebates'
              columns={rebateColumns}
              emptyTitle={t('No rebate records yet')}
            />
          </CardContent>
        </Card>
      )}
    </>
  )
}

function ReferralTable<T extends { id: number }>(props: {
  userId: number
  resource: 'invitees' | 'rebates'
  columns: ColumnDef<T>[]
  emptyTitle: string
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
  })
  const { table } = useDataTable({
    data: result.data?.items ?? [],
    columns: props.columns,
    getRowId: (row) => String(row.id),
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
