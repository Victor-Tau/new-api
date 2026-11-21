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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { SettingsCard } from '../components/settings-card'
import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import type { UpdateOptionResponse } from '../types'

export function ReferralSettingsSection(props: {
  defaultRateBps: number
  exchangeRates: string
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useForm({
    defaultValues: {
      rate: props.defaultRateBps / 100,
      exchangeRates: props.exchangeRates,
    },
  })
  const save = useMutation({
    mutationFn: async (values: { rate: number; exchangeRates: string }) => {
      const response = await api.put<UpdateOptionResponse>(
        '/api/option/referral',
        {
          rate_bps: Math.round(values.rate * 100),
          exchange_rates: values.exchangeRates.trim(),
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
  const submit = form.handleSubmit((values) => save.mutate(values))

  return (
    <SettingsForm onSubmit={submit}>
      <SettingsPageFormActions
        onSave={submit}
        isSaving={save.isPending}
        isSaveDisabled={!form.formState.isDirty}
      />
      <SettingsCard title={t('Referral rebates')}>
        <fieldset disabled={save.isPending} className='space-y-5'>
          <div className='grid max-w-xs gap-2'>
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
          <div className='grid gap-2'>
            <Label htmlFor='referral-exchange-rates'>
              {t('Rebate exchange rates')}
            </Label>
            <Textarea
              id='referral-exchange-rates'
              aria-describedby='referral-exchange-rates-description'
              placeholder='{"EUR": 0.92}'
              className='font-mono'
              required
              {...form.register('exchangeRates', { required: true })}
            />
            <p
              id='referral-exchange-rates-description'
              className='text-muted-foreground text-sm'
            >
              {t(
                'Enter currency units per US dollar as JSON. USD is fixed at 1. CNY uses the site exchange rate by default.'
              )}
            </p>
          </div>
        </fieldset>
      </SettingsCard>
    </SettingsForm>
  )
}
