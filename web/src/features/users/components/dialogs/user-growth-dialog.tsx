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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { handleServerError } from '@/lib/handle-server-error'

import { adjustUserGrowth } from '../../api'
import {
  growthAdjustmentSchema,
  type GrowthAdjustmentValues,
} from '../../lib/user-form'
import type { UserGrowthAdjustment } from '../../types'

type UserGrowthDialogProps = {
  userId: number
  amount?: number
  onSuccess: (result: UserGrowthAdjustment) => void
}

export function UserGrowthDialog(props: UserGrowthDialogProps) {
  const { t } = useTranslation()
  const formId = useId()
  const [open, setOpen] = useState(false)
  const form = useForm<GrowthAdjustmentValues>({
    resolver: zodResolver(growthAdjustmentSchema),
    defaultValues: { amount: '' },
  })

  const save = useMutation({
    mutationFn: (amount: number) => adjustUserGrowth(props.userId, amount),
    onSuccess: (result) => {
      props.onSuccess(result)
      toast.success(t('Update successful'))
      setOpen(false)
    },
    onError: (error) =>
      handleServerError(error, t('Failed to adjust growth points')),
  })

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (save.isPending) return
        if (nextOpen && props.amount !== undefined) {
          form.reset({ amount: String(props.amount) })
        }
        setOpen(nextOpen)
      }}
      trigger={
        <Button
          type='button'
          variant='outline'
          disabled={props.amount === undefined}
        >
          {t('Adjust growth points')}
        </Button>
      }
      title={t('Adjust growth points')}
      description={t(
        'This adjustment counts toward the current VIP growth window.'
      )}
      footer={
        <Button type='submit' form={formId} disabled={save.isPending}>
          {save.isPending ? t('Saving...') : t('Save')}
        </Button>
      }
    >
      <Form {...form}>
        <form
          id={formId}
          noValidate
          onSubmit={(event) => {
            // The dialog is portaled from inside the user edit form.
            event.stopPropagation()
            void form.handleSubmit((values) => {
              if (!save.isPending) save.mutate(Number(values.amount))
            })(event)
          }}
        >
          <FormField
            control={form.control}
            name='amount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Growth points')}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    type='number'
                    min={0}
                    step='0.01'
                    disabled={save.isPending}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
