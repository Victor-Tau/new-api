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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Controller, useForm, useWatch } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { JsonEditor } from '@/components/json-editor'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api } from '@/lib/api'
import { formatQuota } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { FormNavigationGuard } from '../components/form-navigation-guard'
import { SettingsCard } from '../components/settings-card'
import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import type { UpdateOptionResponse } from '../types'

export function ReferralSettingsSection(props: {
  defaultRateBps: number
  exchangeRates: string
  inviterQuota: number
  inviteeQuota: number
  invitationDelayHours: number
  rebateDelayHours: number
  complianceConfirmed: boolean
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const defaultValues = {
    rate: props.defaultRateBps / 100,
    exchangeRates: props.exchangeRates,
    inviterQuota: props.inviterQuota,
    inviteeQuota: props.inviteeQuota,
    invitationDelayHours: props.invitationDelayHours,
    rebateDelayHours: props.rebateDelayHours,
  }
  const form = useForm({ defaultValues })
  const save = useMutation({
    mutationFn: async (values: typeof defaultValues) => {
      const response = await api.put<UpdateOptionResponse>(
        '/api/option/referral',
        {
          rate_bps: Math.round(values.rate * 100),
          exchange_rates: values.exchangeRates.trim() || '{}',
          inviter_quota: values.inviterQuota,
          invitee_quota: values.inviteeQuota,
          invitation_delay_hours: values.invitationDelayHours,
          rebate_delay_hours: values.rebateDelayHours,
        }
      )
      return requireServerSuccess(response.data)
    },
    onSuccess: (_, values) => {
      form.reset(values)
      void queryClient.invalidateQueries({ queryKey: ['system-options'] })
      toast.success(t('Setting updated successfully'))
    },
    onError: (error) => handleServerError(error, t('Failed to update setting')),
  })
  const submit = form.handleSubmit(
    (values) => save.mutate(values),
    () => toast.error(t('Invalid parameters'))
  )
  const [inviterQuota, inviteeQuota] = useWatch({
    control: form.control,
    name: ['inviterQuota', 'inviteeQuota'],
  })
  const quotaFields = [
    {
      name: 'inviterQuota' as const,
      label: t('Inviter Reward'),
      description: t(
        'Quota given to users who invite others ({{formattedQuota}})',
        {
          formattedQuota: formatQuota(inviterQuota || 0),
        }
      ),
    },
    {
      name: 'inviteeQuota' as const,
      label: t('Invitee Reward'),
      description: t('Quota given to invited users ({{formattedQuota}})', {
        formattedQuota: formatQuota(inviteeQuota || 0),
      }),
    },
  ]
  const registerDelay = (name: 'invitationDelayHours' | 'rebateDelayHours') =>
    form.register(name, {
      valueAsNumber: true,
      required: true,
      min: 0,
      max: 2147483647,
      validate: Number.isInteger,
    })

  return (
    <SettingsForm onSubmit={submit}>
      <FormNavigationGuard when={form.formState.isDirty} />
      <SettingsPageFormActions
        onSave={submit}
        isSaving={save.isPending}
        isSaveDisabled={!form.formState.isDirty}
      />
      {!props.complianceConfirmed && (
        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'Non-zero invitation rewards require compliance confirmation in Payment Gateway settings.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <SettingsCard title={t('Invitation rewards')}>
        <fieldset
          disabled={save.isPending}
          className='grid gap-5 md:grid-cols-2'
        >
          {quotaFields.map((field) => (
            <div key={field.name} className='grid max-w-xs gap-2'>
              <Label htmlFor={field.name}>{field.label}</Label>
              <Input
                id={field.name}
                type='number'
                min={0}
                max={Number.MAX_SAFE_INTEGER}
                required
                aria-describedby={`${field.name}-description`}
                {...form.register(field.name, {
                  valueAsNumber: true,
                  required: true,
                  min: 0,
                  validate: Number.isSafeInteger,
                })}
              />
              <p
                id={`${field.name}-description`}
                className='text-muted-foreground text-sm'
              >
                {field.description}
              </p>
            </div>
          ))}
          <div className='grid max-w-xs gap-2'>
            <Label htmlFor='invitation-delay-hours'>
              {t('Inviter reward delay (hours)')}
            </Label>
            <Input
              id='invitation-delay-hours'
              type='number'
              min={0}
              step={1}
              required
              {...registerDelay('invitationDelayHours')}
            />
            <p className='text-muted-foreground text-sm'>
              {t('Counted from registration. Enter 0 for immediate credit.')}
            </p>
          </div>
        </fieldset>
      </SettingsCard>
      <SettingsCard title={t('Referral rebates')}>
        <fieldset disabled={save.isPending} className='space-y-5'>
          <div className='grid gap-5 md:grid-cols-2'>
            <div className='grid max-w-xs content-start gap-2'>
              <Label htmlFor='referral-default-rate'>
                {t('Default rebate rate (%)')}
              </Label>
              <Input
                id='referral-default-rate'
                type='number'
                min={0}
                max={100}
                step='0.01'
                required
                {...form.register('rate', {
                  valueAsNumber: true,
                  min: 0,
                  max: 100,
                  required: true,
                })}
              />
            </div>
            <div className='grid max-w-xs gap-2'>
              <Label htmlFor='rebate-delay-hours'>
                {t('Top-up rebate delay (hours)')}
              </Label>
              <Input
                id='rebate-delay-hours'
                type='number'
                min={0}
                step={1}
                required
                aria-describedby='rebate-delay-hours-description'
                {...registerDelay('rebateDelayHours')}
              />
              <p
                id='rebate-delay-hours-description'
                className='text-muted-foreground text-sm'
              >
                {t(
                  'Counted from payment confirmation or code redemption. Enter 0 for immediate credit.'
                )}
              </p>
            </div>
          </div>
          <fieldset className='space-y-2'>
            <legend className='text-sm font-medium'>
              {t('Rebate exchange rates')}
            </legend>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Enter currency units per US dollar. USD is fixed at 1. CNY uses the site exchange rate by default.'
              )}
            </p>
            <Controller
              control={form.control}
              name='exchangeRates'
              render={({ field }) => (
                <JsonEditor
                  value={field.value}
                  onChange={field.onChange}
                  disabled={save.isPending}
                  keyLabel={t('Currency')}
                  valueLabel={t('Units per USD')}
                  keyPlaceholder='EUR'
                  valuePlaceholder='0.92'
                  valueType='number'
                />
              )}
            />
          </fieldset>
        </fieldset>
      </SettingsCard>
    </SettingsForm>
  )
}
